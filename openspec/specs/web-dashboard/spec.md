## Purpose

Dashboard — главная страница web UI, список pipeline runs с фильтрацией.

## Requirements

### Requirement: Dashboard page
Frontend ДОЛЖЕН отображать Dashboard с списком pipeline runs.

#### Scenario: Отображение списка
- **КОГДА** пользователь открывает `/`
- **ТОГДА** Dashboard ДОЛЖЕН показать список pipeline runs
- **И** каждый run содержит: feature name, status badge, started_at, duration
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Фильтрация по статусу
- **КОГДА** пользователь выбирает фильтр статуса (running/completed/failed/blocked)
- **ТОГДА** Dashboard ДОЛЖЕН показать только runs с выбранным статусом

#### Scenario: Пустой Dashboard
- **КОГДА** нет pipeline runs
- **ТОГДА** Dashboard ДОЛЖЕН показать сообщение: "No pipeline runs found"

### Requirement: Навигация
Frontend ДОЛЖЕН поддерживать навигацию между страницами.

#### Scenario: Клик по pipeline run
- **КОГДА** пользователь кликает по pipeline run в Dashboard
- **ТОГДА** frontend ДОЛЖЕН перейти на `/pipelines/:id`