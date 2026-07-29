## Purpose

Спецификация определяет нормативное поведение capability `ci-pipeline`.
## Requirements
### Requirement: GitHub Actions workflow
Система MUST иметь GitHub Actions workflow для автоматической проверки.
Зафиксированный frontend dependency graph MUST проходить high-severity
dependency audit без подавления или исключения advisory.

#### Scenario: Запуск на push
- **КОГДА** происходит push в master
- **ТОГДА** workflow MUST запустить `go build ./cmd/ai-team`
- **И** запустить unit и E2E tests
- **И** запустить race detector и coverage gate не ниже 60%
- **И** запустить `go vet ./...`
- **И** проверить gofmt и module checksums
- **И** строго валидировать все OpenSpec contracts pinned-версией инструмента
- **И** проверить frontend build, актуальность embedded dist, lint и tests
- **И** выполнить `npm audit --audit-level=high` без advisory exceptions
- **И** выполнить Go vulnerability scan pinned-версией govulncheck
- **И** все перечисленные шаги MUST завершиться успешно для зафиксированного
  dependency graph

#### Scenario: Запуск на pull request
- **КОГДА** создаётся PR в master
- **ТОГДА** workflow MUST запустить те же шаги

### Requirement: Badge
Репозиторий MUST иметь CI-badge в README.

#### Scenario: Badge в шапке
- **КОГДА** пользователь открывает README
- **ТОГДА** в начале файла MUST быть badge с статусом CI

### Requirement: Local verify matches CI
Команда `make verify` MUST выполнять gofmt, coverage gate и end-to-end tests в
дополнение к остальным проверкам, чтобы локальный запуск покрывал те же gates,
что и CI jobs `lint`, `unit-tests`, `race-tests`, `e2e-tests` и `frontend`.

#### Scenario: Unformatted file
- **WHEN** tracked `.go` file не отформатирован gofmt
- **THEN** `make verify` MUST завершиться ошибкой и перечислить такие файлы

#### Scenario: Coverage below threshold
- **WHEN** суммарное test coverage ниже CI threshold
- **THEN** `make verify` MUST завершиться ошибкой

#### Scenario: E2E test failure
- **WHEN** end-to-end test завершается ошибкой
- **THEN** `make verify` MUST завершиться ошибкой

#### Scenario: Полная frontend verification
- **КОГДА** contributor запускает `make verify` на зафиксированном dependency
  graph
- **ТОГДА** команда MUST выполнить `npm ci`
- **И** выполнить frontend build, lint и tests
- **И** проверить, что build не изменил предварительно сохранённое состояние
  `web/dist`
- **И** выполнить `npm audit --audit-level=high` без advisory exceptions
- **И** завершиться успешно, если все frontend gates прошли
