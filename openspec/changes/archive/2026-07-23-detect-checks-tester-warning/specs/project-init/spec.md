## MODIFIED Requirements

### Requirement: Обновлённый конфиг по умолчанию
Конфиг по умолчанию MUST включать `effort`, stage timeout и стек-специфичные deterministic checks.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать:
  - `pipeline:` с именами агентов
  - `cli: opencode`
  - `effort: medium`
  - `stage_timeout: 30m`

#### Scenario: Go stack
- **КОГДА** init обнаруживает Go project
- **ТОГДА** он MUST добавить required `go-test-json` check без shell-интерполяции
- **И** test command MUST отключать test cache через `-count=1`

#### Scenario: Stack без typed parser
- **КОГДА** init обнаруживает Rust, Python или Node project без поддержанного typed adapter
- **ТОГДА** он MUST NOT добавлять untyped command как доказательство тестов
- **И** MUST вывести warning

#### Scenario: Неизвестный стек
- **КОГДА** init не может определить verification profile
- **ТОГДА** он MUST вывести warning
- **И** delivery MUST оставаться запрещённым до настройки required unit/integration/e2e check

#### Scenario: Стек определён, но нет подходящей стадии
- **WHEN** init распознаёт известный стек (например, Go), но в pipeline нет стадии `tester`, к которой можно присвоить checks
- **THEN** init MUST вывести warning, отдельный от warning для нераспознанного стека, явно называющий обнаруженный профиль и отсутствие стадии `tester`
- **AND** delivery MUST оставаться запрещённым до ручной настройки checks
