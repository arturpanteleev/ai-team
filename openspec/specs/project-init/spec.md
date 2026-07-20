## Purpose

Спецификация определяет нормативное поведение capability `project-init`.

## Requirements
### Requirement: Конфиг по умолчанию
Система MUST создавать `.ai-team/config.yaml` с разумными значениями по умолчанию.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать:
  - `schema_version: 3`
  - `pipeline: <list of agents>`
  - `cli: opencode`
  - `effort: medium`
  - explicit checkpoint policies

### Requirement: Кастомный путь конфига
Система MUST поддерживать флаг `--target` для указания директории целевого проекта.

#### Scenario: Init в кастомной директории
- **КОГДА** пользователь запускает `ai-team init --target /path/to/project`
- **ТОГДА** система MUST создать `/path/to/project/.ai-team/` вместо `./.ai-team/`

### Requirement: Gitignore
Система MUST добавлять `.ai-team/` в `.gitignore`, если его там нет.

#### Scenario: Авто-добавление в gitignore
- **КОГДА** `ai-team init` запускается и `.gitignore` существует
- **ТОГДА** система MUST дописать `.ai-team/` в `.gitignore`, если его там ещё нет

### Requirement: Директория reports при инициализации
Система MUST создавать `.ai-team/reports/` при `ai-team init`.

#### Scenario: Init создаёт reports
- **КОГДА** `ai-team init` запускается
- **ТОГДА** система MUST создать `.ai-team/reports/` директорию
- **И** `.ai-team/reports/` MUST быть добавлена в `.gitignore`

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
