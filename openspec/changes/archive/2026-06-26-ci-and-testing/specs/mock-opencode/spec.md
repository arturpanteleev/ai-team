## ДОБАВЛЕННЫЕ Требования

### Требование: Mock opencode
Система ДОЛЖНА предоставить скрипт-заглушку для opencode.

#### Сценарий: Mock создаёт файлы
- **КОГДА** mock-opdecode получает `--message-file prompt.md`
- **ТОГДА** парсит prompt.md на предмет того, какой агент запущен
- **И** создаёт output-артефакты (пустые/шаблонные) по путям из промпта

#### Сценарий: Mock для Analyst
- **КОГДА** в prompt.md есть "analyst" или "System Analyst"
- **ТОГДА** mock ДОЛЖЕН создать proposal.md + specs/product/spec.md

#### Сценарий: Mock для Deployer
- **КОГДА** в prompt.md есть "deployer" или "Deployer"
- **ТОГДА** mock ДОЛЖЕН прочитать review.md и test-report.md
- **И** если review не APPROVED или test-report не PASS — выйти с кодом 1

### Требование: Путь к mock
Mock ДОЛЖЕН лежать в `testdata/mock-opencode.sh`.

#### Сценарий: Обнаружение по PATH
- **КОГДА** `testdata/` добавлен в PATH
- **ТОГДА** команда `opencode` ДОЛЖНА вызывать mock вместо реального opencode
