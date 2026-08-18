# Architecture Decision Records

Ця папка містить ADR-и (Architecture Decision Records) — короткі документи, які фіксують ключові архітектурні рішення проєкту.

## Що таке ADR

ADR — це immutable-запис **одного** архітектурного рішення:

- **Контекст** — стан речей і обмеження на момент прийняття
- **Розглянуті варіанти** — що було на столі, з плюсами і мінусами
- **Прийняте рішення** — що обрано і чому
- **Наслідки** — позитивні і негативні, які цей вибір тягне за собою

ADR-и не редагуються після прийняття. Якщо рішення змінюється — створюється новий ADR зі статусом `Accepted`, а старий помічається як `Superseded by ADR-NNNN`.

## Список ADR-ів

| #    | Назва                                                                                            | Статус   |
|------|--------------------------------------------------------------------------------------------------|----------|
| 001 | [Вибір мови програмування і HTTP-фреймворку](001-go-with-gin-as-thin-http-framework.md)         | Прийнято |
| 002 | [Архітектура — моноліт із пошаровим розділенням](002-monolith-with-layered-architecture.md)     | Прийнято |
| 003 | [Доступ до бази даних через sqlx замість ORM](003-sqlx-instead-of-orm.md)                       | Прийнято |
| 004 | [Сканер релізів — внутрішня goroutine](004-goroutine-scanner.md)                                | Прийнято |
| 005 | [Кешування GitHub API через Redis із TTL 10 хвилин](005-redis-caching-for-github-api.md)        | Прийнято |
| 006 | [Прокидання context.Context через всю call chain](006-context-propagation-through-call-chain.md)| Прийнято |
| 007 | [Структуроване логування (slog) і конвеєр логів до Elasticsearch](007-structured-logging-and-log-pipeline.md) | Прийнято |
| 008 | [RED-метрики, Prometheus і Grafana](008-red-metrics-prometheus-grafana.md)                      | Прийнято |
| 009 | [Вибір брокера повідомлень — RabbitMQ](009-message-broker-rabbitmq.md)                          | Прийнято |
| 010 | [Розподілена транзакція підписки через оркестровану Saga](010-orchestrated-saga-for-subscribe.md)| Прийнято |
| 011 | [gRPC як опційний синхронний транспорт для confirmation-кроку](011-grpc-for-confirmation-transport.md)| Прийнято |
| 012 | [Порядок publish→record на release-шляху](012-publish-then-record-on-release-path.md)            | Прийнято |

## Як додавати нові ADR

1. Скопіюй наявний ADR як шаблон, зміни номер і назву
2. Додай рядок до таблиці у цьому README
3. Зроби PR і прив'яжи мінімум одного рев'юера
