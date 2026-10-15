## Purpose

Dashboard — главная страница web UI («Очередь»): что сейчас в работе и что ждёт
решения, список pipeline runs с фильтрацией. Доска по этапам и список всех
задач живут на странице «Задачи», создание задачи — на странице «Новая задача».
Контракт страницы задачи, словаря действий и ролей — в `web-task-ui`.
## Requirements
### Requirement: Dashboard page
Frontend MUST отображать Dashboard с списком pipeline runs.

#### Scenario: Отображение списка
- **КОГДА** пользователь открывает `/`
- **ТОГДА** Dashboard MUST показать список pipeline runs под заголовком «Очередь»
- **И** каждый run содержит: feature name, status badge, started_at, duration
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Фильтрация по статусу
- **КОГДА** пользователь выбирает фильтр доменного run status, включая running/completed/completed_with_warnings/failed/blocked/stopped/interrupted
- **ТОГДА** Dashboard MUST показать только runs с выбранным статусом

#### Scenario: Пустой Dashboard
- **КОГДА** нет pipeline runs
- **ТОГДА** Dashboard MUST сказать, что задач пока нет
- **И** для роли с правом `run:start` MUST назвать первый шаг — «Новая задача»

#### Scenario: Ошибка загрузки
- **КОГДА** список runs не удалось получить
- **ТОГДА** Dashboard MUST показать ошибку с причиной и кнопку «Повторить загрузку»
- **И** MUST NOT выглядеть как пустая очередь

### Requirement: Навигация
Frontend MUST поддерживать навигацию между страницами.

#### Scenario: Клик по pipeline run
- **КОГДА** пользователь кликает по pipeline run в Dashboard
- **ТОГДА** frontend MUST перейти на `/pipelines/:id`

#### Scenario: Одно слово — одна страница
- **КОГДА** пользователь смотрит на меню
- **ТОГДА** «Очередь» MUST вести на `/`, а «Задачи» — на доску и список по этапам
- **И** никакие два пункта меню или заголовка страниц MUST NOT называться одинаково

### Requirement: История запуска пагинируется
API и frontend MUST использовать bounded pagination, а общее число runs MUST передаваться отдельно.

#### Scenario: Первая страница
- **КОГДА** Dashboard запрашивает runs
- **ТОГДА** запрос MUST содержать bounded limit и non-negative offset
- **И** API MUST вернуть `X-Total-Count`

### Requirement: New run control
Frontend MUST создавать задачу одним путём — страницей «Новая задача» с проектом,
шаблоном, режимом, текстом идеи и внешними ссылками; Dashboard MUST NOT содержать
второй, урезанный вариант формы.

#### Scenario: Команда принята
- **КОГДА** пользователь отправляет валидную форму «Новая задача»
- **ТОГДА** UI MUST показать зарезервированный run_id и ссылку на задачу
- **И** MUST обновлять состояние из WebSocket/projection

#### Scenario: Право создавать
- **КОГДА** роль пользователя не имеет права `run:start` (владелец продукта и архитектор его имеют)
- **ТОГДА** UI MUST NOT показывать кнопку «Новая задача»
- **И** MUST назвать роли, которые могут создать задачу

### Requirement: Waiting status
Dashboard MUST явно отличать `waiting_for_approval` от failed и stopped.

#### Scenario: Pending approval
- **КОГДА** run ждёт решения
- **ТОГДА** карточка MUST показать отдельный status и ссылку на controls

### Requirement: Readiness перед запуском

Dashboard MUST показывать preflight report рядом с кнопкой «Новая задача».

#### Scenario: Required check failed

- **КОГДА** readiness имеет failed required check
- **ТОГДА** dashboard MUST показать конкретную проверку

#### Scenario: Неизвестная готовность

- **КОГДА** проверка окружения не вернула результат
- **ТОГДА** состояние MUST выглядеть нейтрально (не как рекомендация и не как отказ)
