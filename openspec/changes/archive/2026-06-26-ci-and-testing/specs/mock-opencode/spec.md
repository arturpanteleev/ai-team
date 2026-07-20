## ДОБАВЛЕННЫЕ Требования

### Requirement: Mock opencode
Система MUST предоставить скрипт-заглушку для opencode.

#### Scenario: Mock создаёт файлы
- **КОГДА** mock-opdecode получает `--message-file prompt.md`
- **ТОГДА** парсит prompt.md на предмет того, какой агент запущен
- **И** создаёт output-артефакты (пустые/шаблонные) по путям из промпта

#### Scenario: Mock для Analyst
- **КОГДА** в prompt.md есть "analyst" или "System Analyst"
- **ТОГДА** mock MUST создать proposal.md + specs/product/spec.md

#### Scenario: Mock для Deployer
- **КОГДА** в prompt.md есть "deployer" или "Deployer"
- **ТОГДА** mock MUST прочитать review.md и test-report.md
- **И** если review не APPROVED или test-report не PASS — выйти с кодом 1

### Requirement: Путь к mock
Mock MUST лежать в `testdata/mock-opencode.sh`.

#### Scenario: Обнаружение по PATH
- **КОГДА** `testdata/` добавлен в PATH
- **ТОГДА** команда `opencode` MUST вызывать mock вместо реального opencode
