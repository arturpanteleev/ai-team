## Purpose

Спецификация определяет нормативное поведение capability `ci-pipeline`.

## Requirements
### Requirement: GitHub Actions workflow
Система MUST иметь GitHub Actions workflow для автоматической проверки.

#### Scenario: Запуск на push
- **КОГДА** происходит push в master
- **ТОГДА** workflow MUST запустить `go build ./cmd/ai-team`
- **И** запустить unit и E2E tests
- **И** запустить race detector и coverage gate не ниже 60%
- **И** запустить `go vet ./...`
- **И** проверить gofmt и module checksums
- **И** строго валидировать все OpenSpec contracts pinned-версией инструмента
- **И** проверить frontend build, lint, tests и high-severity dependency audit
- **И** выполнить Go vulnerability scan pinned-версией govulncheck

#### Scenario: Запуск на pull request
- **КОГДА** создаётся PR в master
- **ТОГДА** workflow MUST запустить те же шаги

### Requirement: Badge
Репозиторий MUST иметь CI-badge в README.

#### Scenario: Badge в шапке
- **КОГДА** пользователь открывает README
- **ТОГДА** в начале файла MUST быть badge с статусом CI
