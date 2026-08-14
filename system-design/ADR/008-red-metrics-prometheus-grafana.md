# ADR-008: RED-метрики, експорт у Prometheus і дашборд у Grafana

**Статус:** Прийнято

**Дата:** 2026-05-30

**Автор:** Yurii Levchenko

## Контекст

Логи (ADR-007) відповідають на питання "що саме сталось з конкретним запитом". Але вони не відповідають на питання "скільки", "як швидко", "чи росте кількість помилок" — для цього потрібні **метрики** (числові часові ряди).

Застосунок уже експонував Prometheus-метрики на `/metrics` (`http_requests_total`, `http_request_duration_seconds`, бізнес-лічильники), але:

- ніхто їх **не скрейпив** — вони нікуди не зберігались і їх не можна було подивитись у часі;
- не було єдиної методології "що саме міряти";
- не було візуалізації.

HW6 вимагає: інструментувати застосунок за методологією **RED** (Rate, Errors, Duration), налаштувати конвеєр експорту в **Prometheus**, і побудувати **Grafana**-дашборд ключових метрик.

Треба прийняти рішення: (1) яку методологію метрик, (2) як експортувати, (3) чим візуалізувати, (4) як конфігурувати dashboard.

## Розглянуті варіанти

### Методологія метрик

- **RED (Rate, Errors, Duration)** — для request-driven сервісів: скільки запитів/сек, скільки фейляться, скільки тривають. Простий, стандартний для HTTP API.
- **USE (Utilization, Saturation, Errors)** — для ресурсів (CPU, диск, черги). Більше про інфраструктуру, ніж про сервіс.
- **Чотири золоті сигнали Google SRE** (latency, traffic, errors, saturation) — RED + saturation. Saturation складно виміряти на цьому масштабі.

### Експорт метрик

- **Prometheus (pull)** — Prometheus сам періодично скрейпить `/metrics`. Вимагається завданням; стандарт для Go (`client_golang`).
- **Push (StatsD / Pushgateway)** — застосунок сам шле метрики. Доречно для коротких job-ів, не для довгоживучого сервісу.

### Візуалізація і конфігурація dashboard

- **Grafana з provisioning (as-code)** — datasource і dashboard описані файлами, Grafana піднімає їх на старті.
- **Grafana з ручним налаштуванням у UI** — клікаєш datasource і панелі руками, потім експортуєш.

## Прийняте рішення

**RED** як методологія. Інструментовано:

| Сигнал | HTTP API | Background scanner |
|---|---|---|
| **Rate** | `http_requests_total{method,path,status}` | `scanner_runs_total` |
| **Errors** | той самий лічильник, фільтр `status=~"5.."` | `scanner_errors_total{stage}` |
| **Duration** | `http_request_duration_seconds` (histogram) | `scanner_cycle_duration_seconds` (histogram) |

HTTP RED збирається middleware-ом (`metrics.GinMiddleware`); `/metrics` виключено з власної статистики, щоб scrape не роздував лічильники. Scanner інструментовано в `internal/scanner` (duration циклу + помилки за стадією: github / tracking / subscribers / notify).

**Prometheus (pull)** для експорту: окремий контейнер скрейпить `app:8080/metrics` кожні 15s (`observability/prometheus/prometheus.yml`).

**Grafana з provisioning** для візуалізації: datasource (Prometheus) і dashboard ("Notifier — Overview": RED + cache hit ratio + scanner health) описані файлами в `observability/grafana/` і піднімаються автоматично. Анонімний доступ — лише для dev.

Усі компоненти (Prometheus, Grafana — як і ES/Kibana/Filebeat) винесені в окремий `docker-compose.observability.yml`, щоб не сповільнювати звичайний dev/test.

## Наслідки

### Позитивні
- **Видимість у часі:** rate/errors/duration як графіки, а не разові числа. Видно тренди ("помилки ростуть").
- **RED покриває і HTTP, і scanner:** background-воркер теж спостережуваний (зростання `scanner_cycle_duration` = сигнал що сканер не встигає за інтервалом).
- **App залишається чистим:** лише експонує `/metrics`; Prometheus сам тягне (pull). Застосунок не знає про Prometheus/Grafana.
- **Відтворюваність:** datasource + dashboard як код — `docker compose up` і дашборд на місці, без ручного імпорту.
- **Cache hit ratio видно наочно** — прямий показник чи працює Redis-кеш (ADR-005).

### Негативні
- **Більше інфраструктури:** +2 контейнери (Prometheus, Grafana). Тому окремий compose-файл.
- **Кардинальність labels:** `path` має бути шаблоном маршруту, не сирим URL — інакше кожен унікальний URL створює нову часову серію і роздуває Prometheus. (Враховано: `c.FullPath()`.)
- **Немає алертингу:** метрики лише візуалізуються; "помилки зросли о 3 ночі" ніхто не помітить без Alertmanager / Grafana alerts (TODO).
- **Security вимкнено в dev:** анонімний доступ до Grafana — лише локально; у проді треба автентифікація.
- **Дашборд як великий JSON:** редагувати руками громіздко; зміни зручніше робити в UI і експортувати назад у файл.
