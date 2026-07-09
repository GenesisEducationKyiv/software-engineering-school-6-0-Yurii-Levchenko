# System Design: GitHub Release Notification API

Сервіс, який дозволяє користувачам підписатися на email-сповіщення про нові релізи GitHub-репозиторіїв. Складається з **двох процесів**: модульний моноліт на Go (HTTP API, фоновий сканер релізів, Saga-оркестратор) і окремий мікросервіс-**notifier**, що надсилає email. Спілкуються **асинхронно через RabbitMQ** (HW7 — виніс notifier; HW8 — брокер; HW9 — оркестрована Saga). HW10 додав **опційний синхронний gRPC-транспорт** для кроку підтвердження (broker лишається дефолтом — ADR-011).

> Архітектурні рішення задокументовані в [/system-design/ADR](./ADR). У цьому документі — загальна картина системи: вимоги, навантаження, компоненти, потоки, масштабування. Пошарова структура + правила залежностей (з тестами) — у [architecture.md](./architecture.md) (HW11).

---

## 1. Вимоги системи

### Функціональні вимоги

- Користувач може підписатися на сповіщення про релізи GitHub-репозиторію (формат `owner/repo`)
- При підписці надсилається email-лист з посиланням для підтвердження (double opt-in)
- Користувач може підтвердити підписку через одноразовий токен у листі
- Користувач може відписатись через токен (без логіну)
- Користувач може побачити список своїх активних (підтверджених) підписок за email-ом
- Сервіс періодично перевіряє GitHub на нові релізи для всіх підтверджених підписок
- При виявленні нового релізу — надсилає email усім підписникам цього репо
- Сервіс перевіряє існування репо на GitHub при підписці та повертає 404 для неіснуючих

### Нефункціональні вимоги

Проєкт навчальний, працює як single-instance в docker-compose. Жорстких SLA немає. Нижче — характеристики, які реалізовано на сьогодні, і цілі, до яких архітектура готова рухатись.

**Реалізовано:**
- **Запуск:** Запуск системи через `docker-compose up --build` повинен виконуватись не більше 30сек.
- **Persistent state:** Postgres зберігає підписки і `last_seen_tag` сканера; рестарт app не втрачає даних
- **Безпека:** опційна API-key автентифікація на `/api/*`; UUID v4 токени (122 біти ентропії — неможливо вгадати); SQL-параметризація через sqlx (anti-injection); `ReadHeaderTimeout: 10s` (anti-Slowloris)
- **Спостережуваність:** Prometheus `/metrics` з кастомними counters і histograms; логи у stdout (наразі plaintext через stdlib `log`)
- **Graceful shutdown:** SIGTERM скасовує сканер і дає 5s HTTP-серверу на завершення in-flight запитів

**Цілі (готовність архітектури, не виміряно):**
- **Затримка API:** subscribe залежить від GitHub API (~200-500ms cache miss, <50ms cache hit); list/unsubscribe не роблять зовнішніх викликів — мають бути <50ms
- **Масштабованість:** оцінка — до 10K активних підписників і 1K унікальних репо без зміни архітектури (не load-tested)

**Відомі gaps (TODO для production):**
- **Email worker pool:** наразі сканер надсилає листи синхронно в одному циклі. При 1K+ підписників на один популярний реліз цикл блокується на хвилини (1K × ~100ms = ~100s) і пропускає наступний tick. Потрібен пул горутин (10-50 одночасних SMTP-з'єднань) із семафором; це знімає блокування і дає реалістичну пропускну здатність на пікових релізах
- **At-least-once email delivery:** ✅ реалізовано (HW8/HW9) — transactional outbox + RabbitMQ (manual ack, DLQ) + Redis-дедуп + resume-sweeper. Лишається email worker-pool (вище) для паралелізації відправок
- **Структуроване логування:** перехід на `slog` або `zap` із JSON-output для агрегації в Datadog/Grafana
- **HTTPS:** треба reverse proxy (Caddy/nginx) перед app у проді
- **Distributed lock на сканер:** для multi-instance розгортання (зараз 1 інстанс — race condition неможливий)
- **Моніторинг і алертинг:** немає (Prometheus метрики експонуються, але нікуди не скрейпляться)
- **Надсилання на email:** надсилати листи не на Mailtrap а на рельний inbox імейл-провайдера

### Обмеження

- **Два процеси:** модульний моноліт (API + Scanner + Saga-оркестратор) і окремий notifier-мікросервіс; зв'язок через RabbitMQ
- **GitHub API rate limits:** 60 req/h без токена, 5000 з токеном
- **SMTP (dev):** листи йдуть у Mailpit (локальний fake-inbox + UI); у проді — SES/SendGrid/Mailgun через env, без зміни коду
- **Бюджет інфраструктури:** docker-compose — app, notifier, postgres, redis, rabbitmq, mailpit

---

## 2. Оцінка навантаження

### Користувачі та трафік

| Метрика | Очікувано | Пік |
|---|---|---|
| Активні підписники | ~5K | 10K |
| Підписок на користувача | 2-5 | 20 |
| Унікальні репо для трекінгу | ~1K | 5K |
| Subscribe-запити (POST /api/subscribe) | 0.1 RPS | 5 RPS |
| List-запити (GET /api/subscriptions) | 0.5 RPS | 10 RPS |
| Релізів за день (на всіх трекнутих репо) | ~50 | 200 |
| Email-сповіщень за день | ~200 | 5K (в день масового релізу) |

> Цифри обрано виходячи з відомих лімітів: GitHub token дає 5000 req/h, що з 10-хвилинним кешем покриває 1K унікальних репо при 5-хвилинному циклі сканера. Postgres і Redis на single instance тримають тисячі QPS — наш пік 10 RPS на цих шарах не помітний. Реальне обмеження — пропускна здатність email-надсилання при бурстах (див. TODO нижче).

### Розмір даних

| Запис | Розмір | На 10K юзерів |
|---|---|---|
| `subscriptions` row | ~150 байт (email + repo + UUID + timestamps) | ~1.5 МБ |
| `repositories` row | ~80 байт (repo + last_seen_tag + timestamp) | ~80 КБ для 1K репо |
| Redis cache entry (`repo_exists`, `latest_release`) | ~50 байт | ~250 КБ для 5K ключів |
| **Загальний обсяг БД після року роботи** | — | **< 50 МБ** |

База мала — будь-який сучасний Postgres-інстанс впорається. Bottleneck — не БД.

### Bandwidth

| Напрямок | Розмір | Оцінка |
|---|---|---|
| Incoming HTTP requests | ~500 байт на запит | <1 KB/s |
| Outgoing GitHub API requests | ~200 байт запит / ~2 КБ відповідь | ~5 KB/s в середньому, мало завдяки кешу |
| Outgoing SMTP (release email) | ~1 КБ на лист | бурстами при релізах, 0 між ними |
| Outgoing SMTP (confirmation) | ~1 КБ на лист | <1 KB/s |

Мережа — теж не Bottleneck.

### Bottleneck

**GitHub API rate limit.** На 1000 унікальних репо і 5-хвилинному циклі сканера без кешу = 12000 req/h, що в 2.4× перевищує ліміт навіть з токеном. Тому Redis-кеш із 10-хвилинним TTL (ADR-005) — критичний компонент, а не nice-to-have.

---

## 3. High-Level архітектура

```mermaid
flowchart LR
    User([User<br/>browser/curl/Postman])
    GH[GitHub API]
    Mailpit[Mailpit<br/>SMTP + UI]

    subgraph "Docker Compose Network"
        App[Monolith<br/>API + Scanner + Saga :8080]
        DB[(PostgreSQL)]
        Redis[(Redis)]
        MQ[[RabbitMQ]]
        Notifier[Notifier<br/>microservice]
    end

    User -->|HTTP /api/*<br/>HTML page /| App
    App -->|sqlx queries| DB
    App -->|cache get/set| Redis
    App -->|HTTPS REST| GH
    App -->|publish commands| MQ
    App -.->|gRPC SendConfirmation<br/>opt-in, sync| Notifier
    MQ -->|consume| Notifier
    Notifier -->|reply sent/failed| MQ
    MQ -->|reply| App
    Notifier -->|dedup| Redis
    Notifier -->|SMTP| Mailpit
    Mailpit -.->|email with link| User
```

### Внутрішня структура моноліту

Notifier тепер — окремий процес (нижче). У моноліті з'явились Saga-оркестратор і transactional outbox; назовні моноліт говорить лише через RabbitMQ.

```mermaid
flowchart TB
    Router[Gin Router]

    subgraph "HTTP layer"
        H1[Subscribe]
        H2[Confirm]
        H3[Unsubscribe]
        H4[List]
        Static[Static / metrics / health]
    end

    Service[subscription.Service<br/>validation, errors]
    Orch[orchestrator<br/>Saga T1 · reply-consumer · sweeper]

    subgraph "Stores (sqlx + Postgres)"
        SubStore[(subscriptions)]
        SagaStore[(saga)]
        OutboxStore[(outbox)]
        TrackStore[(repositories)]
    end

    Cached[CachedClient<br/>Redis wrapper]
    GHClient[GitHub Client<br/>net/http]
    Publisher[BrokerPublisher]
    Relay[Outbox relay<br/>goroutine]
    Scanner[Scanner goroutine]
    MQ[[RabbitMQ]]

    Router --> H1 & H2 & H3 & H4 & Static
    H1 & H2 & H3 & H4 --> Service
    Service --> SubStore & Cached & Orch
    Orch --> SubStore & SagaStore & OutboxStore
    Cached --> GHClient
    Relay --> OutboxStore
    Relay --> Publisher
    Scanner --> TrackStore & Cached & Publisher
    Publisher -->|AMQP| MQ
    MQ -->|replies| Orch
```

---

## 4. Детальний дизайн компонентів

### 4.1 API Service (Gin)

**Відповідальність:**
- Приймати HTTP-запити, парсити JSON-body і URL-параметри
- Валідувати вхідні дані (email format, required fields)
- Викликати Service-шар і мапати business errors на HTTP-статуси
- Сервити статичну HTML-сторінку на `/` для browser-based підписки
- Експортувати Prometheus-метрики на `/metrics`

**Endpoints:**
```
GET    /                            HTML subscription page
GET    /health                      health probe → {"status":"ok"}
GET    /metrics                     Prometheus metrics
POST   /api/subscribe               body: {"email":"...","repo":"owner/repo"}
GET    /api/confirm/:token          confirm via UUID token
GET    /api/unsubscribe/:token      unsubscribe via UUID token
GET    /api/subscriptions?email=..  list active subscriptions
```

**Обробка помилок** (мапінг на HTTP-статуси через `errors.Is`):
- `ErrInvalidEmail`, `ErrInvalidRepoFormat` → 400
- `ErrRepoNotFound` → 404
- `ErrAlreadySubscribed` → 409
- `ErrTokenNotFound` → 404
- інше → 500

**Middleware-стек:** logger → recovery → metrics-collector → optional API-key auth → handler.

Middleware — це функції, які обгортають handler і виконуються до/після нього. Порядок важливий: пройти через увесь стек запит має згори донизу, відповідь — у зворотному напрямку. Наш порядок:

- **logger** (Gin) — записує метод, шлях, статус, тривалість кожного запиту в stdout. Стоїть першим, щоб логувати все, включно з тими запитами, які впадуть далі по стеку
- **recovery** (Gin) — ловить `panic()` і конвертує в HTTP 500 замість краху всього процесу. Без нього один баг в одному handler-і вбиває весь сервер
- **metrics-collector** — записує `http_requests_total` і `http_request_duration_seconds` для Prometheus (див. секцію 4.7)
- **API-key auth** (опційний, тільки якщо `API_KEY` env-змінна задана) — перевіряє заголовок `X-API-Key`. Якщо ключа немає або він невірний — 401/403, handler не виконується
- **handler** — фінальна точка, твій бізнес-код

### 4.2 Service layer (бізнес-логіка)

**Відповідальність:**
- Валідація email через regex
- Парсинг `owner/repo` формату
- Координація: валідація + перевірка repo на GitHub, тоді **делегування** створення підписки Saga-оркестратору (ADR-010) — сам Service не пише підписку й не публікує в брокер
- Генерація UUID токенів
- Визначення доменних помилок (`ErrXxx`)

**Ключові методи:**
```go
Subscribe(ctx, email, repo) error
Confirm(token) error
Unsubscribe(token) error
GetSubscriptions(email) ([]Subscription, error)
```

`Subscribe` приймає `context.Context` для прокидання у GitHub-клієнт (ADR-006).

### 4.3 Repository layer (Postgres + sqlx)

**Відповідальність:** усі SQL-запити до БД через `sqlx`.

**Дві таблиці:**

```mermaid
erDiagram
    SUBSCRIPTIONS {
        int id PK
        varchar email
        varchar repo
        varchar token UK
        boolean confirmed
        varchar status
        timestamp created_at
    }
    REPOSITORIES {
        int id PK
        varchar repo UK
        varchar last_seen_tag
        timestamp last_checked_at
        timestamp last_release_at
    }
    SAGA {
        uuid id PK
        int subscription_id
        varchar email
        varchar repo
        varchar state
        int attempts
        text last_error
        timestamp created_at
        timestamp updated_at
    }
    OUTBOX {
        bigserial id PK
        uuid saga_id
        varchar routing_key
        varchar message_id
        jsonb payload
        timestamp created_at
        timestamp published_at
    }
```

`subscriptions.status` (`pending`/`confirmed`/`failed`) і таблиці `saga`/`outbox` додані в HW9. `saga` — стан кожної субскрайб-саги (одна на спробу); `outbox` — команди «на відправку», що relay публікує в брокер.

**Ключові обмеження:**
- `UNIQUE(email, repo)` у `subscriptions` — заборона дублікатів (повторна підписка на `failed`-репо реактивує той самий рядок)
- `UNIQUE(token)` — токен як гарантовано одиничний confirm/unsubscribe URL
- `UNIQUE(repo)` у `repositories` — один state-рядок на репо
- partial index `outbox(id) WHERE published_at IS NULL` — relay читає лише неопубліковані

**Запити:**
- Subscriptions: CreateInTx / GetByToken / GetByEmailAndRepo / Confirm / Delete / GetSubscriptionsByEmail / GetActiveRepos / GetSubscribersByRepo / MarkFailed / ReactivateInTx
- Repositories: GetRepoTracking / RegisterRepo / TouchLastChecked / RecordRelease (UPSERT через `ON CONFLICT DO UPDATE`)
- Saga: CreateInTx / GetByID / UpdateState / FindResumable
- Outbox: Enqueue / FetchUnpublished / MarkPublished / Requeue

**Міграції:** `golang-migrate` запускається на старті застосунку, читає `migrations/*.up.sql` і виконує нові.

### 4.4 GitHub Client (із Redis-обгорткою)

**Caching Strategy** (двошарова через інтерфейс — див. ADR-005):

- **L1 (Redis):** TTL 10 хв для `repo_exists:owner/repo` і `latest_release:owner/repo`
- **L2 (none, raw call to GitHub API)** — використовується при cache miss або коли Redis недоступний
- **Fallback:** якщо Redis відключений на старті — `main.go` підставляє raw `Client` без кешу і логує warning

**Rate Limit Handling:**

```go
// Експоненціальний backoff на 429
maxRetries := 3
for i := 0; i < maxRetries; i++ {
    resp, err := c.httpClient.Do(req)
    if resp.StatusCode != http.StatusTooManyRequests {
        return resp, nil
    }
    backoff := time.Duration(2<<i) * time.Second  // 2s, 4s, 8s
    time.Sleep(backoff)
}
```

**Опційний токен:** якщо `GITHUB_TOKEN` встановлено — додається у заголовок `Authorization: Bearer ...`, ліміт зростає з 60 до 5000 req/h.

### 4.5 Scanner (background goroutine)

**Відповідальність:** періодичний обхід усіх активних репо і виявлення нових релізів.

**Запуск:** з `main.go` через `go releaseScanner.Start(ctx)`. `ctx` — головний контекст застосунку, скасовується при SIGTERM/SIGINT.

**Цикл:**
```go
ticker := time.NewTicker(5 * time.Minute)
for {
    select {
    case <-ctx.Done():
        return // graceful shutdown
    case <-ticker.C:
        scan(ctx)
    }
}
```

Кожен `scan(ctx)` циклу:
1. `repo.GetActiveRepos()` → список унікальних `owner/repo` із підтверджених підписок
2. Для кожного: `github.GetLatestRelease(ctx, owner, repo)` (через кеш)
3. Порівняти з `repositories.last_seen_tag` у БД
4. Якщо тег новий — `RecordRelease(repo, tag)` + для кожного підписника **опублікувати release-команду в RabbitMQ** (`BrokerPublisher`); надсилає лист уже notifier

**Масштабування:** на 1 інстансі. Кілька реплік без додаткової координації призвели б до дублікатних повідомлень — потрібен distributed lock (Redis або Postgres advisory lock) перед майбутнім multi-instance розгортанням.

### 4.6 Notifier (окремий мікросервіс, RabbitMQ consumer)

**Це окремий процес** (`cmd/notifier`, свій контейнер) — не частина моноліта. Споживає команди з RabbitMQ і надсилає email через `net/smtp` (єдине місце в системі, що знає про SMTP).

- **Consume:** черга `notifications`, **manual ack** — ack лише після успішної відправки; краш до ack → redelivery (at-least-once).
- **Idempotency:** Redis-ключ `processed:{message_id}` (TTL 24h) — дубль не шле другий лист.
- **DLQ:** «отруйні» повідомлення (битий JSON / невідомий routing key) → `notifications.dlq`.
- **Retry:** тимчасовий збій SMTP — кілька спроб з backoff; потім reply `failed`.
- **Reply:** після відправки (або вичерпання спроб) публікує `SagaReply{saga_id, sent|failed}` назад оркестратору.
- **Два типи листів:** `SendConfirmationEmail` (після підписки) і `SendReleaseNotification` (новий реліз).
- **Конфіг через env:** `SMTP_*`, `RABBITMQ_URL`, `REDIS_URL`. Dev — Mailpit; прод — SES/SendGrid/Mailgun без зміни коду.

### 4.7 Observability (Prometheus + structured logs)

**Метрики на `/metrics`:**
- HTTP: `http_requests_total{method,path,status}`, `http_request_duration_seconds`
- Бізнес: `subscriptions_created_total`, `subscriptions_confirmed_total`, `unsubscribes_total`
- Сканер: `scanner_runs_total`, `releases_detected_total`, `notifications_sent_total`
- GitHub: `github_api_calls_total{endpoint, cache="hit|miss"}` — пряма видимість ефективності кешу

**Логи:** stdout структурований (через `log` stdlib + Gin debug logger). У продакшні треба переходити на `slog` або `zap` із JSON-output для агрегації.

### 4.8 Saga-оркестратор (підписка)

Підписка виконується як **оркестрована Saga** (ADR-010): «створити підписку» і «надіслати лист» — у різних сервісах, а лист незворотний, тож координуємо кроки з компенсацією.

- **T1** (одна Postgres-транзакція): `INSERT subscription(pending)` + `INSERT saga(subscription_created)` + `INSERT outbox(команда)` — атомарно, без dual-write.
- **T2:** notifier надсилає лист (ідемпотентно) і відповідає `sent`/`failed`.
- **Завершення:** reply `sent` → saga `completed`.
- **Компенсація (C1):** reply `failed` (після retry) → `compensating` → підписка `failed` → saga `failed` (рядок не видаляємо).
- **Resume-sweeper:** періодично/на старті дотягує застряглі саги — re-drive `subscription_created` (загублений reply) або довершення `compensating` (краш). Відновлення без ручного втручання.
- **Реактивація:** повторна підписка на `failed`-репо оживляє той самий рядок (новий токен → `pending`) + нова сага; стара лишається як історія.

### 4.9 Transactional outbox + relay

Прибирає dual-write «запис у БД + publish у брокер» (борг із ADR-009):

- Команду пишемо рядком у `outbox` **тією ж транзакцією**, що й бізнес-дані (крок T1).
- **Relay** (горутина в моноліті) опитує `outbox WHERE published_at IS NULL`, публікує в RabbitMQ, ставить `published_at`. Порядок publish→mark → at-least-once (consumer дедупить); краш між кроками → republish наступного тіку.

### 4.10 gRPC як опційний синхронний транспорт confirmation (HW10)

Крок «надіслати лист-підтвердження» може йти **синхронно через gRPC** замість async-брокера — обирається конфігом `CONFIRMATION_TRANSPORT` (`broker` за замовчуванням | `grpc`). Це транспортна альтернатива поруч, **не заміна**: async лишається продакшн-дефолтом (ADR-011). gRPC — це синхронний RPC поверх HTTP/2 + Protobuf; додаємо його заради типізованого контракту й порівняння REST-vs-gRPC, а не щоб повертати доставку на sync (це б регресувало розв'язку HW8/HW9).

- **Контракт:** `notifier.v1.NotifierService.SendConfirmation` (unary), опис у `proto/notifier/v1/notifier.proto`, кодоген через `buf` (remote-плагіни), згенерований код у `gen/` закомічено.
- **Сервер:** notifier піднімає gRPC-сервер на окремому порту (`:50051`) поряд з AMQP-консюмером; хендлер перевикористовує той самий `SMTPSender` + Redis-дедуп.
- **Клієнт:** оркестратор залежить від інтерфейсу `confirmationSender` (DIP). У grpc-режимі крок T1 пише лише `subscription + saga` (**без outbox-рядка** — інакше relay опублікував би команду й лист пішов би двічі), тоді синхронно кличе notifier і завершує/компенсує сагу **inline** (без async-reply).
- **Status codes:** `OK` → sent; `InvalidArgument` → порожні поля; `Unavailable` → SMTP впав. Оркестратор компенсує на будь-який не-OK — той самий ефект, що й async-reply `failed`.
- **API-контракт незмінний:** subscribe повертає той самий результат обома транспортами; збій видно через subscription `status` (не через HTTP-код). Різниця sync — лише в тому, що сага стає термінальною миттєво.
- **Компроміс:** sync знімає чергу/relay/reply, але прив'язує subscribe до uptime notifier і **не відновлюється sweeper'ом** при краші між T1 і inline-апдейтом (немає outbox-рядка). Тому дефолт — broker.
- **Порівняння REST vs gRPC** (throughput, latency, байти на дроті) — харнес `cmd/confirmbench` + таблиця в root README. Стисло: на localhost throughput ≈ паритет, gRPC ~2× легший трафік (HPACK + Protobuf) і тісніший p99.

---

## 5. Ключові потоки

### 5.1 Subscribe flow

Підписка — це Saga: синхронна частина (валідація + перевірки + крок T1 однією транзакцією) дає відповідь одразу; відправка листа доробляється асинхронно.

> 🎨 Візуальна (редагована) схема цього потоку — [`saga-flow.excalidraw`](./saga-flow.excalidraw): outbox → relay → RabbitMQ → **notifier-мікросервіс** + saga-reply-петля. Відкрий на [excalidraw.com](https://excalidraw.com) → Open.

```mermaid
sequenceDiagram
    participant U as User
    participant H as Handler
    participant S as Service
    participant O as Orchestrator
    participant DB as Postgres
    participant Relay
    participant MQ as RabbitMQ
    participant N as Notifier

    U->>H: POST /api/subscribe {email, repo}
    H->>S: Subscribe(ctx, email, repo)
    S->>S: validate email + repo format
    S->>S: CheckRepoExists (cached GitHub) — sync, потрібна відповідь зараз
    S->>DB: duplicate? (email+repo) → reactivate if failed
    Note over S,DB: T1 — одна транзакція
    S->>O: StartConfirmation(...)
    O->>DB: INSERT subscription(pending) + saga + outbox cmd
    S-->>H: nil
    H-->>U: 200 "check your email"
    Note over Relay,N: async — вже після відповіді
    Relay->>DB: poll outbox (unpublished)
    Relay->>MQ: publish confirmation command
    MQ->>N: deliver
    N->>N: dedup + SMTP send (→ Mailpit)
    N->>MQ: reply sent
    MQ->>O: reply consumer
    O->>DB: saga → completed
```

> Шлях збою: notifier не зміг надіслати → reply `failed` → оркестратор компенсує (підписка `failed`); зависла сага → resume-sweeper дотягує.

> gRPC-варіант (opt-in, ADR-011, §4.10): кроки Relay→MQ→N→reply зникають — оркестратор кличе notifier **синхронно** й завершує/компенсує сагу inline; T1 при цьому не пише outbox.

### 5.2 Scanner cycle (every 5 min)

```mermaid
sequenceDiagram
    participant T as Ticker
    participant SC as Scanner
    participant DB as Postgres
    participant C as CachedClient
    participant R as Redis
    participant G as GitHub API
    participant MQ as RabbitMQ

    T->>SC: tick (every 5m)
    SC->>DB: SELECT DISTINCT repo<br/>WHERE confirmed=true
    DB-->>SC: [repo1, repo2, ...]
    loop for each repo
        SC->>C: GetLatestRelease(ctx, owner, repo)
        C->>R: GET latest_release:owner/repo
        alt cache HIT
            R-->>C: "v1.12.0"
        else cache MISS
            C->>G: GET /releases/latest
            G-->>C: {tag_name: "v1.12.0"}
            C->>R: SET (TTL 10m)
        end
        C-->>SC: "v1.12.0"
        SC->>DB: SELECT last_seen_tag<br/>WHERE repo=?
        DB-->>SC: "v1.11.0"
        Note over SC: tags differ → new release
        SC->>DB: UPSERT repositories<br/>SET last_seen_tag="v1.12.0"
        SC->>DB: SELECT email, token<br/>WHERE repo=? AND confirmed=true
        DB-->>SC: [subscribers...]
        loop for each subscriber
            SC->>MQ: publish release command (BrokerPublisher)
        end
    end
```

> Release-сповіщення публікуються в брокер напряму (без saga/outbox) — notifier їх консюмить так само, як confirmation. Saga обгортає лише subscribe-флоу.

---

## 6. Масштабування

Система проєктована під ~10K підписників. Що зміниться при 10×, 100× і 1000×:

| Масштаб | Що ламається першим | Як адаптувати |
|---|---|---|
| **100K підписників** | Email throughput у синхронному циклі сканера (один лист = ~100ms через SMTP × 1000 підписників на популярний реліз = 100s) | Винести email-надсилання у worker-pool горутин, або в окрему чергу повідомлень |
| **1M підписників, 50K репо** | GitHub API rate limit; БД-навантаження на сканер-запит | Розділити репо між кількома GitHub-токенами; partitioning таблиці `subscriptions` за hash(email); replica для читань |
| **10M підписників** | Неминуче розщеплення на сервіси | Винести Scanner у окремий сервіс із distributed lock; винести Notifier за чергою (SQS/RabbitMQ); Postgres → керована БД із read replicas |

**Поточна архітектура має чіткі точки для горизонтального масштабування**, оскільки всі stateful-компоненти (Postgres, Redis) уже зовнішні відносно app. Скейлити можна спочатку вертикально, потім — реплікація app із distributed lock на сканер.

---

## 7. Failure modes

| Падає | Що бачить юзер | Як деградує |
|---|---|---|
| **PostgreSQL** | API повертає 500 на всі endpoints | Сервіс непрацездатний — це critical dependency |
| **Redis** | API працює як завжди | Кеш missing → прямі виклики GitHub → ризик rate limit при високому навантаженні. Логується warning |
| **GitHub API** (5xx або downtime) | Subscribe може провалитися (404 на CheckRepoExists інтерпретується як неіснуюче репо — TODO: розрізнити 5xx vs 404) | Сканер логує помилку і йде далі; на наступному циклі спробує знову |
| **SMTP / notifier лежить** | Subscribe все одно успішний (лист у черзі), НЕ 500 | Команда чекає в черзі; notifier retry; остаточний збій → saga компенсує (підписка `failed`, видно юзеру). Релізні листи теж чекають у черзі |
| **RabbitMQ лежить** | Subscribe успішний (команда збережена в outbox) | Relay не може опублікувати → ретраїть; брокер встав → публікує. Reply-consumer перепідключається. Нічого не губиться |
| **GitHub rate limit (429)** | Subscribe може повільно відповісти (експ. backoff) або провалитися | Експ. backoff 2s/4s/8s, потім помилка |
| **Контейнер app падає** | API недоступний на час рестарту | Docker `restart: on-failure` піднімає за секунди; підписки не втрачаються, бо стан у БД |
| **Краш посеред саги** | — | Resume-sweeper на старті/періодично дотягує застряглі саги (re-drive або компенсація) |

**Що поки не покрито**
- Email worker-pool — відправки в notifier поки послідовні (backoff блокує єдину consume-горутину); ок при малому обсязі
- Distributed lock на сканер — multi-instance розгортання спричинить дублікати
- Сага, зависла понад TTL дедупу (24h): re-drive sweeper'а може повторно надіслати лист (рідкісний край)

---

## 8. Безпека

| Surface | Захист | Стан |
|---|---|---|
| API endpoints | Опційний `X-API-Key` header (через `API_KEY` env) | Реалізовано (middleware) |
| Subscription token | UUID v4 (122 біти ентропії, неможливий брутфорс) | Реалізовано |
| SQL injection | sqlx параметризовані запити (`$1, $2`) | Реалізовано |
| Email validation | Regex + Gin's binding `email` validator | Реалізовано |
| Slowloris attack | `ReadHeaderTimeout: 10s` на http.Server | Реалізовано (виправлено в HW#1) |
| HTTPS | Поки out of scope — рекомендовано reverse proxy (Caddy/nginx) у проді | Не реалізовано |
| Secret management | `.env` файл, у `.gitignore`; `.env.example` для шаблону | Реалізовано |
| Rate limiting (per-IP) | Не реалізовано | TODO для проду |

---

## 9. Технологічний стек

| Шар | Технологія | Чому | ADR |
|---|---|---|---|
| Мова | Go 1.26 | Статика, goroutines, alignment з Genesis | [001](./ADR/001-go-with-gin-as-thin-http-framework.md) |
| HTTP | Gin | Тонкий router з валідацією | [001](./ADR/001-go-with-gin-as-thin-http-framework.md) |
| Архітектура | Модульний моноліт + notifier-мікросервіс | Чіткий поділ, async-розв'язка | [002](./ADR/002-monolith-with-layered-architecture.md) |
| ORM | sqlx (raw SQL) | Прозорість, контроль | [003](./ADR/003-sqlx-instead-of-orm.md) |
| БД | PostgreSQL 16 | Надійність, зрілість | [003](./ADR/003-sqlx-instead-of-orm.md) |
| Міграції | golang-migrate | SQL-файли, runs on startup | [003](./ADR/003-sqlx-instead-of-orm.md) |
| Шедулер | In-process goroutine | Без зовнішньої інфраструктури | [004](./ADR/004-goroutine-scanner.md) |
| Кеш + дедуп | Redis 7 | TTL з коробки; також idempotency-дедуп у notifier | [005](./ADR/005-redis-caching-for-github-api.md) |
| Брокер | RabbitMQ 3 | надійна черга задач: ack, DLQ, routing | [009](./ADR/009-message-broker-rabbitmq.md) |
| Розподілена транзакція | Orchestrated Saga + transactional outbox | консистентність subscribe↔email без 2PC | [010](./ADR/010-orchestrated-saga-for-subscribe.md) |
| gRPC-транспорт (опційний) | gRPC + Protobuf, контракт через buf | типізований sync-транспорт для confirmation + REST-vs-gRPC порівняння | [011](./ADR/011-grpc-for-confirmation-transport.md) |
| Email (dev) | Mailpit (SMTP + UI) | локальний fake-inbox | — |
| Тестування | Go testing + interfaces з моками | Без зовнішніх залежностей у тестах | [006](./ADR/006-context-propagation-through-call-chain.md) |
| Метрики | Prometheus client_golang | Стандарт індустрії | — |
| Контейнеризація | Docker + docker-compose | Reproducible setup | — |
| CI | GitHub Actions + golangci-lint | Стандарт для GitHub-проєктів | — |

---

## 10. Що я покращу далі

- **Email worker-pool у notifier** — паралельні SMTP-відправки замість послідовних (зняти head-of-line blocking)
- **Distributed lock на сканер** через Redis SETNX або Postgres advisory lock — для multi-instance розгортання
- **Структуроване логування через `slog`** замість stdlib `log` — JSON-output для агрегації в Datadog/Grafana
- **OpenTelemetry tracing** — використати наявне context propagation, додати spans у GitHub-клієнт і SMTP
- **Integration-тести з testcontainers** — реальні Postgres + Redis у тестах
- ✅ **gRPC-транспорт для confirmation** (HW10, ADR-011) — реалізовано як opt-in sync-альтернатива з `buf`-контрактом і бенчмарком; далі можна винести й release-нотифікації або дати gRPC для зовнішнього API
- **Rate limiting per IP** — захист від abuse на subscribe
- **HTML email templates** — наразі plain text
