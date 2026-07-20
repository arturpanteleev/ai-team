## Purpose

Спецификация определяет нормативное поведение capability `agent-architect`.

## Requirements
### Requirement: Architect создаёт технический дизайн
Агент Architect MUST прочитать продуктовую спецификацию и создать технический дизайн.

#### Scenario: Architect создаёт артефакты
- **КОГДА** Architect запускается
- **ТОГДА** он MUST прочитать `.ai-team/artifacts/{feature}/specs/`
- **И** создать `.ai-team/artifacts/{feature}/design.md`
- **И** создать `.ai-team/artifacts/{feature}/tasks.md`

### Requirement: Содержимое технического дизайна
Технический дизайн MUST содержать архитектуру, модели данных, API-контракты и план реализации.

#### Scenario: Секции дизайна
- **КОГДА** Architect создаёт design.md
- **ТОГДА** он MUST включать:
  - Обзор архитектуры
  - Модели данных / схемы
  - API-контракты
  - Разбивку на компоненты
  - Выбор технологий
  - Задачи по реализации в tasks.md

### Requirement: Список задач
tasks.md MUST содержать чеклист задач по реализации.

#### Scenario: Формат задач
- **КОГДА** Architect создаёт tasks.md
- **ТОГДА** каждая задача MUST быть чекбоксом markdown: `- [ ] Описание задачи`
- **И** задачи MUST быть упорядочены по зависимостям

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
