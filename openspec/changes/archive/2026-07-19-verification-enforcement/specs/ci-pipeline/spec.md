## ADDED Requirements

### Requirement: Проверка форматирования
CI MUST проверять gofmt-соответствие.

#### Scenario: Файл не отформатирован
- **КОГДА** в PR есть Go-файл, не соответствующий gofmt
- **ТОГДА** CI-джоб MUST упасть с перечислением файлов

### Requirement: Сборка frontend
CI MUST собирать web-frontend.

#### Scenario: Frontend build
- **КОГДА** запускается CI
- **ТОГДА** MUST выполниться `npm ci && npm run build` в `web/`
- **И** ошибки типов TypeScript MUST ронять джоб
