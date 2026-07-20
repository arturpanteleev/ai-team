## Purpose

Спецификация определяет нормативное поведение capability `agent-analyst`.

## Requirements
### Requirement: System Analyst создаёт продуктовую спецификацию
Агент Analyst MUST прочитать описание задачи и создать детальную продуктовую спецификацию.

#### Scenario: Analyst создаёт артефакты
- **КОГДА** Analyst запускается
- **ТОГДА** он MUST прочитать `.ai-team/artifacts/tasks/{feature}/task.md`
- **И** создать `.ai-team/artifacts/{feature}/proposal.md`
- **И** создать `.ai-team/artifacts/{feature}/specs/`
- **И** создать `.ai-team/artifacts/{feature}/specs/product/spec.md`

### Requirement: Содержимое продуктовой спецификации
Продуктовая спецификация MUST содержать пользовательские истории, критерии приёмки и все пользовательские сценарии.

#### Scenario: Секции спецификации
- **КОГДА** Analyst создаёт specs
- **ТОГДА** spec MUST включать:
  - Пользовательские истории с персонами
  - Критерии приёмки (тестируемые)
  - Happy path, крайние случаи, сценарии ошибок
  - Бизнес-правила и ограничения
  - Тестовые сценарии для QA

### Requirement: Analyst использует AgentCLI runtime
Агент Analyst MUST использовать `agentcli` runtime.

#### Scenario: Настройка runtime
- **КОГДА** Analyst загружается
- **ТОГДА** def.yaml MUST содержать `runtime: agentcli` и `cli: opencode`

### Requirement: Промпт analyst
Промпт analyst MUST содержать требования к структуре proposal.md и spec.md.

#### Scenario: Структура proposal.md
- **КОГДА** analyst создаёт proposal.md
- **ТОГДА** proposal.md MUST содержать: бизнес-проблему, scope и out-of-scope, зафиксированные продуктовые требования, спорные моменты, Acceptance Criteria

#### Scenario: Структура AC
- **КОГДА** analyst создаёт Acceptance Criteria
- **ТОГДА** AC MUST описывать наблюдаемое поведение системы
- **И** включать: успешные сценарии, ошибки и невалидные данные, значимые edge cases, поведение, которое MUST NOT измениться

#### Scenario: Формат spec.md
- **КОГДА** analyst создаёт spec.md
- **ТОГДА** spec.md MUST быть в формате OpenSpec (markdown с заголовками ## ADDED Requirements)
