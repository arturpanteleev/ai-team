## Purpose

HTTP-сервер для web UI — chi router, static file serving, CORS.

## Requirements

### Requirement: HTTP-сервер
Система ДОЛЖНА предоставить HTTP-сервер для обслуживания API и статических файлов.

#### Scenario: Запуск сервера
- **КОГДА** пользователь запускает `ai-team web --port 8080`
- **ТОГДА** HTTP-сервер ДОЛЖЕН запуститься на порту 8080
- **И** обслуживать API endpoints на `/api/*`
- **И** обслуживать статические файлы frontend на `/`

#### Scenario: Порт по умолчанию
- **КОГДА** порт не указан
- **ТОГДА** сервер ДОЛЖЕН использовать порт 8080

#### Scenario: Graceful shutdown
- **КОГДА** пользователь нажимает Ctrl+C
- **ТОГДА** сервер ДОЛЖЕН корректно завершить работу

### Requirement: SPA fallback
Сервер ДОЛЖЕН поддерживать клиентскую маршрутизацию (SPA).

#### Scenario: Несуществующий маршрут
- **КОГДА** клиент запрашивает маршрут, не являющийся API или статическим файлом
- **ТОГДА** сервер ДОЛЖЕН вернуть index.html для обработки клиентским роутером

### Requirement: CORS middleware
Сервер ДОЛЖЕН поддерживать CORS для development frontend.

#### Scenario: CORS headers
- **КОГДА** frontend запущен отдельно (localhost:5173)
- **ТОГДА** сервер ДОЛЖЕН добавлять CORS headers для dev requests