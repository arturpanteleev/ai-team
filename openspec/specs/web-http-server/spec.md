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
CLI MUST по умолчанию bind web server к `127.0.0.1`. Every route — REST API
and WebSocket alike — MUST reject any request whose Host header, or whose
Origin header when present, does not resolve to a loopback hostname
(`127.0.0.1`, `localhost` or `::1`). Comparing Origin to Host is not
sufficient, since DNS rebinding makes both headers agree with each other
while still reflecting an attacker-controlled domain.

#### Scenario: Cross-origin browser request
- **WHEN** запрос приходит с постороннего browser origin
- **ТОГДА** сервер MUST NOT добавлять permissive CORS headers
- **AND** WebSocket upgrade MUST быть отклонён

#### Scenario: Rebind-style REST request
- **WHEN** an HTTP request's Host header names a non-loopback hostname (regardless of what IP it actually routed through)
- **THEN** every route, including the REST API, MUST reject it with 403

#### Scenario: Rebind-style Origin with a loopback Host
- **WHEN** a request's Host header is loopback but its Origin header names a non-loopback hostname
- **THEN** the request MUST be rejected with 403

