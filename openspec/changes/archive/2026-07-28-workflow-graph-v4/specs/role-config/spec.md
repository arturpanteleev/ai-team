## MODIFIED Requirements

### Requirement: Strict schema

Config loader MUST отклонять неизвестные и дублирующиеся поля, дополнительные
YAML documents, повторяющиеся этапы, неизвестные graph references,
неоднозначные edges, недостижимые узлы и неограниченные циклы.

#### Scenario: Опечатка поля

- **КОГДА** config содержит `gate_afer`
- **ТОГДА** загрузка MUST завершиться ошибкой до создания run

### Requirement: Обратная совместимость

Отсутствующий `schema_version` MUST интерпретироваться как legacy version 1;
schemas 1–3 MUST компилироваться в legacy graph, а новые конфиги MUST
сериализоваться с schema version 4.

#### Scenario: Pipeline как массив строк

- **КОГДА** legacy config содержит `pipeline: [analyst, coder]`
- **ТОГДА** строки MUST быть нормализованы в AgentConfig с глобальными fallback values
- **И** compiler MUST создать линейное ребро analyst → coder

### Requirement: Ролевые overrides

Каждый pipeline node MUST поддерживать `name`, `model`, `effort`, `cli`,
`timeout` и `checks`; schema v4 MUST хранить route, retries и human approval
roles/quorum/actions только в `workflow`.

#### Scenario: Global fallback

- **КОГДА** model, effort или cli не задан на этапе
- **ТОГДА** MUST использоваться соответствующее глобальное значение

#### Scenario: Approval policy schema v4

- **КОГДА** schema v4 задаёт human roles на pipeline node вместо edge
- **ТОГДА** config MUST быть отклонён как неоднозначный источник route policy

#### Scenario: Default workflow

- **КОГДА** создаётся default config
- **ТОГДА** смысловые AI edges MUST иметь human roles для product,
  architecture, development, review и QA ответственности
