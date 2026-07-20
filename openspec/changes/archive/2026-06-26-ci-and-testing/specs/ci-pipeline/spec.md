## ДОБАВЛЕННЫЕ Требования

### Requirement: GitHub Actions workflow
Система MUST иметь GitHub Actions workflow для автоматической проверки.

#### Scenario: Запуск на push
- **КОГДА** происходит push в main
- **ТОГДА** workflow MUST запустить `go build ./cmd/ai-team`
- **И** запустить `go test ./...`
- **И** запустить `go vet ./...`

#### Scenario: Запуск на pull request
- **КОГДА** создаётся PR в main
- **ТОГДА** workflow MUST запустить те же шаги

### Requirement: Badge
Репозиторий MUST иметь CI-badge в README.

#### Scenario: Badge в шапке
- **КОГДА** пользователь открывает README
- **ТОГДА** в начале файла MUST быть badge с статусом CI
