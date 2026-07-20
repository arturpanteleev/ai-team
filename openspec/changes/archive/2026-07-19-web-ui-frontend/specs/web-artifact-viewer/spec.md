## ADDED Requirements

### Requirement: Artifact viewer
Frontend MUST отображать содержимое артефактов.

#### Scenario: Markdown рендеринг
- **КОГДА** пользователь открывает артефакт `.md`
- **ТОГДА** Artifact Viewer MUST отрендерить markdown как HTML
- **И** сохранить форматирование (заголовки, списки, код, таблицы)

#### Scenario: Raw view
- **КОГДА** пользователь нажимает "Raw"
- **ТОГДА** Artifact Viewer MUST показать исходный markdown текст

#### Scenario: Навигация назад
- **КОГДА** пользователь нажимает "Back"
- **ТОГДА** frontend MUST вернуться на страницу pipeline detail

### Requirement: Маршрутизация
Frontend MUST поддерживать прямой доступ к артефактам.

#### Scenario: Прямая ссылка
- **КОГДА** пользователь открывает `/artifacts/:path`
- **ТОГДА** Artifact Viewer MUST загрузить и отобразить артефакт
