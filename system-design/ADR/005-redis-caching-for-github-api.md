# ADR-005: Кешування відповідей GitHub API через Redis із TTL 10 хвилин

**Статус:** Прийнято

**Дата:** 2026-05-08

**Автор:** Yurii Levchenko

## Контекст

Сервіс активно ходить у GitHub API:

- **Subscribe-flow:** при кожному `POST /api/subscribe` викликається `CheckRepoExists(owner, repo)` — перевірка існування репо
- **Scanner-flow:** кожні 10 хвилин для кожного активного репо —
  `GetLatestRelease(owner, repo)`
...
**Інтервал сканера вирівняно з TTL кешу** свідомо: при `scan_period = TTL` кожен цикл сканера завжди отримує свіжі дані з GitHub (cache miss), а кеш слугує перш за все subscribe-flow — коли кілька користувачів підписуються на популярний репо (`golang/go`, `gin-gonic/gin`) протягом 10-хв вікна, другий і подальші виклики читають з кешу. Якщо б `scan` був менший за TTL — половина циклів сканера читала б застарілі дані з кешу без жодної переваги в API-budget (приклад: 5/10 еквівалентний 10/10 за числом викликів, але вдвічі частіше крутить scanner-loop на хості).

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
    if err != nil {
        return false, err // не кешуємо на помилці
    }
    cc.cache.Set(key, fmt.Sprintf("%v", exists))
    return exists, nil
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

Префікси (`repo_exists:`, `latest_release:`) дозволяють легко групувати/шукати ключі. Для prod-Redis з великим keyspace — `SCAN 0 MATCH repo_exists:*` (non-blocking, cursor-based). `KEYS` блокує single-threaded Redis і застосовується лише для локального дебагу з малим keyspace.

## Наслідки

### Позитивні
- **Дедуплікація викликів у subscribe-flow:** коли кілька користувачів підписуються на однаковий репо протягом 10хв вікна, лише перший виклик іде на GitHub — решта читають з кешу. Для scanner кеш не дає переваг (при `scan_period = TTL` кожен цикл — cache miss), бо scanner не повторюється на одному репо в межах одного циклу — кеш-економія можлива тільки на повторних реквестах про той самий репо.
- **Persistence через рестарти:** Redis зберігає закешовані ключі між рестартами app — після перезапуску попередньо отримані відповіді ще валідні поки не вичерпається їх TTL, не треба заново заповнювати кеш через перші виклики до GitHub.
- **Метрики hit/miss:** Prometheus-counter `github_api_calls_total{cache="hit|miss"}` дозволяє відстежувати ефективність кешу в продакшні
- **Резильєнтність:** якщо Redis впаде — сервіс продовжує роботу (graceful degradation)
- **Простий debug:** `docker exec redis redis-cli KEYS '*'` показує всі ключі (`KEYS '*'` теж працює локально, але у проді обовʼязково `SCAN`)

### Негативні
- **Додатковий контейнер:** docker-compose тепер має +1 сервіс (app, db, *redis*)
- **Можлива stale-data вікно:** якщо хтось видалив репо, ми про це дізнаємось через до 10 хвилин. Прийнятно для цього use case
- **Ще один компонент, який може впасти:** додає surface area для production failures (але через graceful fallback не так критично)
- **Memory footprint:** Redis тримає ключі в RAM. На 1000 ключах це <1MB — норм для невеликого масштабу проєкта