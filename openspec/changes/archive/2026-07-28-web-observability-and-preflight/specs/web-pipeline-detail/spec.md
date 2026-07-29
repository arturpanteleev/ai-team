## MODIFIED Requirements

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

## ADDED Requirements

### Requirement: Live-лог attempt

Frontend MUST показывать bounded tail immutable лога выбранного attempt.

#### Scenario: Активный attempt раскрыт

- **КОГДА** пользователь раскрывает running attempt
- **ТОГДА** frontend MUST периодически получать новые данные его лога
- **И** lifecycle state MUST продолжать приходить через WebSocket

#### Scenario: Attempt завершён

- **КОГДА** выбранный attempt terminal
- **ТОГДА** frontend MUST прекратить периодическое обновление после финального чтения
