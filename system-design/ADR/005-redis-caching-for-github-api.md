# ADR-005: Кешування відповідей GitHub API через Redis із TTL 10 хвилин

**Статус:** Прийнято

**Дата:** 2026-05-08

**Автор:** Yurii Levchenko

## Контекст

Сервіс активно ходить у GitHub API:

- **Subscribe-flow:** при кожному `POST /api/subscribe` викликається `CheckRepoExists(owner, repo)` — перевірка існування репо
- **Scanner-flow:** кожні 5 хвилин для кожного активного репо — `GetLatestRelease(owner, repo)`

GitHub має жорсткі rate limits:

- **Без токена:** 60 запитів/годину для IP
- **З токеном:** 5000 запитів/годину для акаунта

Якщо у нас 100 активних репо, сканер сам по собі робитиме 100 запитів кожні 5 хвилин = **1200/годину** — без кешу це 24% від ліміту, лишається мало запасу для пікових subscribe-сценаріїв.

Додаткові виклики `CheckRepoExists` для популярних репо (`golang/go`, `gin-gonic/gin`) повторюються — те саме питання задаємо щоразу і отримуєм ту саму відповідь.

Завдання вимагає кеш із TTL 10 хвилин для відповідей GitHub API.

## Розглянуті варіанти

### 1. Без кешу — пряме звернення до GitHub
- **Плюси:** Нуль додаткової інфраструктури. Завжди свіжі дані.
- **Мінуси:** Швидке вичерпання rate limit при зростанні бази підписок. Повторні однакові виклики марно витрачають ліміт.

### 2. In-memory cache (sync.Map або хеш-мапа з мьютексом)
- **Плюси:** Швидко (немає мережевих latency). Простий API. Нуль зовнішніх залежностей.
- **Мінуси:** Втрачається при рестарті процесу. При горизонтальному масштабуванні кожен інстанс має власний кеш — нижча hit rate. Складніше моніторити hit/miss метрики.

### 3. Redis із TTL
- **Плюси:** Переживає рестарти. Спільний для майбутніх горизонтальних реплік. Має вбудований TTL — нічого не треба чистити вручну.
- **Мінуси:** Додатковий контейнер у docker-compose. Network latency до Redis (мізерна для localhost: <1ms).

## Прийняте рішення

Обрано **Redis із TTL 10 хвилин** (відповідно до вимоги задачі).

Реалізація — обгортка `CachedClient` навколо `github.Client`, що реалізує той самий інтерфейс:

```go
type CachedClient struct {
    client *Client
    cache  Cacher
}

func (cc *CachedClient) CheckRepoExists(ctx context.Context, owner, repo string) (bool, error) {
    key := fmt.Sprintf("repo_exists:%s/%s", owner, repo)
    if val, _ := cc.cache.Get(key); val != "" {
        return val == "true", nil // cache HIT
    }
    // cache MISS — call GitHub
    exists, err := cc.client.CheckRepoExists(ctx, owner, repo)
    cc.cache.Set(key, fmt.Sprintf("%v", exists))
    return exists, err
}
```

Service- і Scanner-шари використовують `CachedClient` як прозорий drop-in replacement через спільний інтерфейс.

**Graceful fallback:** якщо Redis недоступний при старті — `main.go` логує warning і використовує raw `Client` без кешу:

```go
redisCache, err := cache.New(cfg.RedisURL, ttl)
if err != nil {
    log.Printf("WARNING: Redis not available, running without cache: %v", err)
    redisCache = nil
}
```

Сервіс продовжує працювати, лише з вищим використанням GitHub-rate-ліміту.

## Структура ключів у Redis

| Ключ | Значення | TTL |
|---|---|---|
| `repo_exists:owner/repo` | `"true"` або `"false"` | 600s |
| `latest_release:owner/repo` | tag string (наприклад `"v1.12.0"`) | 600s |

Префікси (`repo_exists:`, `latest_release:`) дозволяють легко групувати/шукати ключі через `KEYS repo_exists:*` під час дебагінгу.

## Наслідки

### Позитивні
- **Знижене навантаження на GitHub API:** при 100 репо і 5-хвилинному циклі — з кешем сканер робитиме лише ~6 виключень за 10-хвилинне вікно (бо TTL 10 хв) замість 200
- **Persistence через рестарти:** після перезапуску сервісу кеш не треба прогрівати
- **Метрики hit/miss:** Prometheus-counter `github_api_calls_total{cache="hit|miss"}` дозволяє відстежувати ефективність кешу в продакшні
- **Резильєнтність:** якщо Redis впаде — сервіс продовжує роботу (graceful degradation)
- **Простий debug:** `docker exec redis redis-cli KEYS '*'` показує всі ключі

### Негативні
- **Додатковий контейнер:** docker-compose тепер має +1 сервіс (app, db, *redis*)
- **Можлива stale-data вікно:** якщо хтось видалив репо, ми про це дізнаємось через до 10 хвилин. Прийнятно для цього use case
- **Ще один компонент, який може впасти:** додає surface area для production failures (але через graceful fallback не так критично)
- **Memory footprint:** Redis тримає ключі в RAM. На 1000 ключах це <1MB — норм для невеликого масштабу проєкта