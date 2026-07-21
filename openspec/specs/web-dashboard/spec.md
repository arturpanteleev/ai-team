## Purpose

Dashboard — главная страница web UI, список pipeline runs с фильтрацией.

## Requirements

### Requirement: Dashboard page
Frontend MUST отображать Dashboard с списком pipeline runs.

#### Scenario: Отображение списка
- **КОГДА** пользователь открывает `/`
- **ТОГДА** Dashboard MUST показать список pipeline runs
- **И** каждый run содержит: feature name, status badge, started_at, duration
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Фильтрация по статусу
- **КОГДА** пользователь выбирает фильтр доменного run status, включая running/completed/completed_with_warnings/failed/blocked/stopped/interrupted
- **ТОГДА** Dashboard MUST показать только runs с выбранным статусом

#### Scenario: Пустой Dashboard
- **КОГДА** нет pipeline runs
- **ТОГДА** Dashboard MUST показать сообщение: "No pipeline runs found"

### Requirement: Навигация
Frontend MUST поддерживать навигацию между страницами.

#### Scenario: Клик по pipeline run
- **КОГДА** пользователь кликает по pipeline run в Dashboard
- **ТОГДА** frontend MUST перейти на `/pipelines/:id`

### Requirement: История запуска пагинируется
API и frontend MUST использовать bounded pagination, а общее число runs MUST передаваться отдельно.

#### Scenario: Первая страница
- **КОГДА** Dashboard запрашивает runs
- **ТОГДА** запрос MUST содержать bounded limit и non-negative offset
- **И** API MUST вернуть `X-Total-Count`
