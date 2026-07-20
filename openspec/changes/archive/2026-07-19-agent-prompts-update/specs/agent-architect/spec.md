## MODIFIED Requirements

### Requirement: Промпт architect
Промпт architect MUST содержать требования к структуре design.md и tasks.md.

#### Scenario: Структура design.md
- **КОГДА** architect создаёт design.md
- **ТОГДА** design.md MUST содержать: затронутые репозитории и компоненты, выбранное техническое решение, изменения по файлам или модулям, изменения контрактов и данных, риски и способы их снижения, порядок реализации

#### Scenario: Структура tasks.md
- **КОГДА** architect создаёт tasks.md
- **ТОГДА** tasks.md MUST содержать: зависимый чеклист задач, каждая задача — чекбокс markdown

#### Scenario: Обнаружение ошибок
- **КОГДА** architect обнаруживает ошибку в requirements
- **ТОГДА** architect MUST вернуть BLOCKED
