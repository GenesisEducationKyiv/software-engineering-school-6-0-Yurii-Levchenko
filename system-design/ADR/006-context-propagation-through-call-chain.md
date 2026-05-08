# ADR-006: Прокидання context.Context через всю call chain

**Статус:** Прийнято

**Дата:** 2026-05-08

**Автор:** Yurii Levchenko

## Контекст

Сервіс робить I/O-операції на кількох рівнях:

- HTTP-запити до GitHub API (`net/http`)
- SQL-запити до PostgreSQL
- SMTP до Mailtrap (поза скоупом цього ADR)
- Redis для кешу

Усі ці операції потенційно тривалі та можуть потребувати скасування ззовні з трьох причин:

1. **Клієнт відключився** — користувач закрив браузер посеред запиту; немає сенсу чекати GitHub
2. **Graceful shutdown** — `docker stop` посилає SIGTERM; потрібно скасувати поточні операції за обмежений час
3. **Deadline зверху** — у майбутньому можна зробити обмеження типу "цей запит не повинен виконуватись довше 3 секунд"

У початковій реалізації `http.NewRequest("GET", url, nil)` створював HTTP-запити **без можливості скасування ззовні**. Лінтер `noctx` із `golangci-lint` поскаржився на це.

Виявляється, це класична архітектурна дилема в Go: контекст — частина сигнатури функції, тому "додати контекст" означає змінити публічний API багатьох шарів.

## Розглянуті варіанти

### 1. Не використовувати context (status quo до фіксу)
- **Плюси:** Менше boilerplate; сигнатури простіші.
- **Мінуси:** Goroutine leak при відключенні клієнта. Graceful shutdown не працює end-to-end. Неможливо встановити deadline. Linter скаржиться (noctx).

### 2. Створювати `context.Background()` всередині кожної функції-приймача
- **Плюси:** Не треба міняти публічні сигнатури.
- **Мінуси:** Перекреслює сенс context — він не пов'язаний із зовнішнім скасуванням. Антипатерн у Go.

### 3. Прокинути `context.Context` через всі шари (handler → service → github client)
- **Плюси:** Кожен виклик можна скасувати ззовні. Ідіоматично для Go (стандартна бібліотека вимагає `context` для довготривалих операцій). Працює з deadlines, traces, request-scoped values.
- **Мінуси:** Cascade-зміна сигнатур через 5+ файлів. Тести потребують `context.Background()` у кожному виклику. Інтерфейси мокування треба оновити.

### 4. Глобальний кореневий context
- **Плюси:** Не треба передавати параметром.
- **Мінуси:** Антипатерн. Неможливо мати per-request deadlines, складно тестувати.

## Прийняте рішення

Обрано **прокидання `context.Context` через всі шари**.

Структура:

```
HTTP request
    │
    ▼
gin.Context (Gin тримає request context)
    │  c.Request.Context()
    ▼
handler.Subscribe(c)
    │
    ▼
service.Subscribe(ctx, email, repo)        ← інтерфейс приймає ctx
    │
    ▼
github.CheckRepoExists(ctx, owner, repo)   ← інтерфейс приймає ctx
    │
    ▼
http.NewRequestWithContext(ctx, ...)       ← request скасовується разом із ctx
```

Те саме для scanner-flow:

```
main.go: ctx, cancel := context.WithCancel(context.Background())
    │
    ▼
go scanner.Start(ctx)
    │
    ▼
scanner.scan(ctx) → checkRepo(ctx, repo)
    │
    ▼
github.GetLatestRelease(ctx, owner, repo)
```

`main.go` встановлює signal handler для SIGTERM/SIGINT, який викликає `cancel()`. Це автоматично:
- Зупиняє цикл сканера на наступній ітерації `select`
- Скасовує всі поточні HTTP-запити до GitHub
- Дозволяє HTTP-серверу gracefully завершити in-flight запити (через окремий `srv.Shutdown(ctx)`)

## Наслідки

### Позитивні
- **Goroutine leak prevention:** клієнт відключився → Gin скасовує `c.Request.Context()` → весь ланцюг до GitHub обривається; goroutine звільняється. На великому масштабі відчутно не займає ресурси.
- **Graceful shutdown працює end-to-end:** SIGTERM зупиняє і HTTP-сервер, і scanner, і всі активні зовнішні запити за <5 секунд
- **Готовність до deadlines:** у будь-якій точці можна обернути `ctx, _ := context.WithTimeout(ctx, 3*time.Second)` — і GitHub-запит виконається максимум 3 секунди
- **Готовність до tracing:** OpenTelemetry і подібні бібліотеки використовують context для прокидання span; зараз архітектура готова до додавання трейсингу (трекання одного запиту поки він проходить через багато компонентів системи) без рефакторингу
- **Ідіоматичний Go:** будь-який Go-розробник одразу впізнає патерн і знає, як ним користуватись

### Негативні
- **Багато сигнатур змінилось:** `Service.Subscribe`, `GitHubClient.CheckRepoExists`, `GitHubClient.GetLatestRelease`, `Scanner.scan`, `Scanner.checkRepo` — усі тепер приймають `ctx` першим параметром
- **Тести стали трохи довшими:** кожен виклик в test-файлах потребує `context.Background()` як перший аргумент. Mock'и теж приймають ctx (і ігнорують його через `_`)
- **DB і SMTP поки не отримують ctx:** repository-методи і notifier ще не приймають ctx (TODO для майбутньої ітерації — використати `db.QueryContext`, `db.ExecContext` через sqlx). Зараз `database/sql` пул сам обробляє таймаути на рівні connection