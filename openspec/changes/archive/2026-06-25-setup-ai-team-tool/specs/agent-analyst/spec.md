## ДОБАВЛЕННЫЕ Требования

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
