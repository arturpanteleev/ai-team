## ADDED Requirements

### Requirement: New run control
Dashboard MUST позволять авторизованному локальному пользователю создать run с
feature и task.

#### Scenario: Команда принята
- **КОГДА** пользователь отправляет валидную форму
- **ТОГДА** UI MUST показать зарезервированный run_id
- **И** MUST обновлять состояние из WebSocket/projection

### Requirement: Waiting status
Dashboard MUST явно отличать `waiting_for_approval` от failed и stopped.

#### Scenario: Pending approval
- **КОГДА** run ждёт решения
- **ТОГДА** карточка MUST показать отдельный status и ссылку на controls
