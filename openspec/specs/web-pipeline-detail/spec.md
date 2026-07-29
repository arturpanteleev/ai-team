## Purpose

Pipeline detail — страница с деталями pipeline run и его stages.
## Requirements
### Requirement: Pipeline detail page

Frontend MUST отображать детали run, attempts, human approvals, live-лог и
controller-owned evidence и обновлять их по versioned live events.

#### Scenario: Отображение этапов, evidence и approvals

- **КОГДА** пользователь открывает `/pipelines/:id`
- **ТОГДА** страница MUST показать run identity, status, attempts,
  checks, mutations, delivery, pending/resolved approvals, actions, roles,
  quorum, subject и decision audit

#### Scenario: Live updates

- **КОГДА** WebSocket отправляет lifecycle или approval event выбранного run
- **ТОГДА** страница MUST запросить актуальную projection без browser reload

### Requirement: Status badges
Frontend MUST отображать цветовые badge для статусов.

#### Scenario: Цвета статусов
- **КОГДА** этап имеет статус
- **ТОГДА** badge MUST быть:
  - running — синий
  - completed — зелёный
  - failed — красный
  - blocked — оранжевый
  - skipped/interrupted/invalidated — отдельный неположительный стиль

### Requirement: Approval controls
Run detail MUST позволять отправить допустимый action с actor, role и
необязательным comment, сохраняя exact subject неизменным.

#### Scenario: Решение принято
- **КОГДА** пользователь выбирает action и required role
- **ТОГДА** UI MUST отправить exact approval/run/subject identity
- **И** MUST предложить resume после resolved status

#### Scenario: Ошибка решения
- **КОГДА** API отклоняет stale, role или conflict decision
- **ТОГДА** UI MUST показать ошибку и перечитать актуальный approval

### Requirement: Live-лог attempt

Frontend MUST показывать bounded tail immutable лога выбранного attempt.

#### Scenario: Активный attempt раскрыт

- **КОГДА** пользователь раскрывает running attempt
- **ТОГДА** frontend MUST периодически получать новые данные его лога
- **И** lifecycle state MUST продолжать приходить через WebSocket

#### Scenario: Attempt завершён

- **КОГДА** выбранный attempt terminal
- **ТОГДА** frontend MUST прекратить периодическое обновление после финального чтения

### Requirement: Визуализация маршрута

Run detail MUST показывать compiled graph, approval policy рёбер и текущую
позицию lifecycle.

#### Scenario: Graph run открыт

- **КОГДА** пользователь открывает detail graph run
- **ТОГДА** UI MUST показать entry, nodes, outcome edges, targets, roles,
  quorum и max_visits
