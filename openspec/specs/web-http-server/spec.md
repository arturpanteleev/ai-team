## Purpose

HTTP-сервер для локального web UI — chi router и static file serving.

## Requirements

### Requirement: HTTP-сервер
Система MUST предоставить HTTP-сервер для обслуживания API и статических файлов.

#### Scenario: Запуск сервера
- **КОГДА** пользователь запускает `ai-team web --port 8080`
- **ТОГДА** HTTP-сервер MUST запуститься на порту 8080
- **И** обслуживать API endpoints на `/api/*`
- **И** обслуживать статические файлы frontend на `/`

#### Scenario: Порт по умолчанию
- **КОГДА** порт не указан
- **ТОГДА** сервер MUST использовать порт 8080

#### Scenario: Graceful shutdown
- **КОГДА** пользователь нажимает Ctrl+C
- **ТОГДА** сервер MUST корректно завершить работу

### Requirement: SPA fallback
Сервер MUST поддерживать клиентскую маршрутизацию (SPA).

#### Scenario: Несуществующий маршрут
- **КОГДА** клиент запрашивает маршрут, не являющийся API или статическим файлом
- **ТОГДА** сервер MUST вернуть index.html для обработки клиентским роутером

### Requirement: Local-only security boundary
CLI MUST по умолчанию bind web server к `127.0.0.1`, а browser WebSocket MUST принимать только same-origin connection.

#### Scenario: Cross-origin browser request
- **КОГДА** запрос приходит с постороннего browser origin
- **ТОГДА** сервер MUST NOT добавлять permissive CORS headers
- **И** WebSocket upgrade MUST быть отклонён
