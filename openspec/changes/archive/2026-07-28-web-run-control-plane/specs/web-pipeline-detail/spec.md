## MODIFIED Requirements

### Requirement: Pipeline detail page
Frontend MUST отображать детали run, attempts и human approvals и обновлять их
по versioned live events.

#### Scenario: Отображение этапов и approvals
- **КОГДА** пользователь открывает `/pipelines/:id`
- **ТОГДА** страница MUST показать run identity, status, attempts,
  pending/resolved approvals, actions, roles, quorum, subject и decision audit

#### Scenario: Live updates
- **КОГДА** WebSocket отправляет lifecycle или approval event выбранного run
- **ТОГДА** страница MUST запросить актуальную projection без browser reload

## ADDED Requirements

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
