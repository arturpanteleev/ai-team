# workflow-graph Specification

## Purpose
Граф workflow schema v4: узлы pipeline, рёбра по outcome с approval policies, terminal targets и max_visits, проверяемые до первого LLM-вызова.
## Requirements
### Requirement: Декларативный outcome graph

Schema v4 MUST задавать entry и уникальные edges по паре source/outcome с
exact node или terminal target.

#### Scenario: Неоднозначное ребро

- **КОГДА** два edges имеют одинаковые source и outcome
- **ТОГДА** config MUST быть отклонён до создания run

#### Scenario: Outcome выбран

- **КОГДА** attempt завершён с recorded outcome
- **ТОГДА** engine MUST выбрать единственное соответствующее ребро
- **И** сохранить его target до следующего AI call

### Requirement: Ограниченные циклы

Каждый узел в цикле MUST иметь положительный `max_visits`.

#### Scenario: Цикл без лимита

- **КОГДА** graph содержит цикл и хотя бы один его узел не имеет max_visits
- **ТОГДА** config MUST быть отклонён

#### Scenario: Лимит исчерпан

- **КОГДА** выбранное ребро ведёт к узлу, чей max_visits уже исчерпан
- **ТОГДА** run MUST завершиться ошибкой до запуска нового attempt

### Requirement: Approval принадлежит ребру

Каждое non-terminal edge schema v4 MUST иметь policy с roles, quorum и exact
action targets.

#### Scenario: Человек выбирает маршрут

- **КОГДА** edge требует approval и решение resolved
- **ТОГДА** engine MUST продолжить к exact target выбранного action
- **И** stale subject или неподходящая роль MUST быть отклонены

### Requirement: Совместимость legacy pipeline

Schemas 1–3 MUST компилироваться в детерминированный graph с прежним
линейным маршрутом, checkpoints и loopback semantics.

#### Scenario: Config schema 3

- **КОГДА** загружен существующий schema 3 config без workflow
- **ТОГДА** он MUST пройти validation
- **И** compiled passed edges MUST следовать порядку pipeline
