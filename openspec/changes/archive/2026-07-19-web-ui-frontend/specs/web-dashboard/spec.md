## ADDED Requirements

### Requirement: Dashboard page
Frontend MUST отображать Dashboard с списком pipeline runs.

#### Scenario: Отображение списка
- **КОГДА** пользователь открывает `/`
- **ТОГДА** Dashboard MUST показать список pipeline runs
- **И** каждый run содержит: feature name, status badge, started_at, duration
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Фильтрация по статусу
- **КОГДА** пользователь выбирает фильтр статуса (running/completed/failed/blocked)
- **ТОГДА** Dashboard MUST показать только runs с выбранным статусом

#### Scenario: Пустой Dashboard
- **КОГДА** нет pipeline runs
- **ТОГДА** Dashboard MUST показать сообщение: "No pipeline runs yet"

### Requirement: Навигация
Frontend MUST поддерживать навигацию между страницами.

#### Scenario: Клик по pipeline run
- **КОГДА** пользователь кликает по pipeline run в Dashboard
- **ТОГДА** frontend MUST перейти на `/pipelines/:id`
