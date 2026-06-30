## ADDED Requirements

### Requirement: Директория reports при инициализации
Система ДОЛЖНА создавать `.ai-team/reports/` при `ai-team init`.

#### Scenario: Init создаёт reports
- **КОГДА** `ai-team init` запускается
- **ТОГДА** система ДОЛЖНА создать `.ai-team/reports/` директорию
- **И** `.ai-team/reports/` ДОЛЖНА быть добавлена в `.gitignore`

### Requirement: Обновлённый конфиг по умолчанию
Конфиг по умолчанию ДОЛЖЕН включать ролевые настройки.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml ДОЛЖЕН содержать:
  - `pipeline:` с полными именами агентов (например `[analyst, architect, coder, reviewer, tester, deployer]`)
  - `cli: opencode`
  - `model: auto`
  - `effort: medium`
