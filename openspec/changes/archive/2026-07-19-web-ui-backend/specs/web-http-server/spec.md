## ADDED Requirements

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

### Requirement: CORS middleware
Сервер MUST поддерживать CORS для development frontend.

#### Scenario: CORS headers
- **КОГДА** frontend запущен отдельно (localhost:5173)
- **ТОГДА** сервер MUST добавлять CORS headers для dev requests
