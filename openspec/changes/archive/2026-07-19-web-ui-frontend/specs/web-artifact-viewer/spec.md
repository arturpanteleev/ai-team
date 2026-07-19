## ADDED Requirements

### Requirement: Artifact viewer
Frontend ДОЛЖЕН отображать содержимое артефактов.

#### Scenario: Markdown рендеринг
- **КОГДА** пользователь открывает артефакт `.md`
- **ТОГДА** Artifact Viewer ДОЛЖЕН отрендерить markdown как HTML
- **И** сохранить форматирование (заголовки, списки, код, таблицы)

#### Scenario: Raw view
- **КОГДА** пользователь нажимает "Raw"
- **ТОГДА** Artifact Viewer ДОЛЖЕН показать исходный markdown текст

#### Scenario: Навигация назад
- **КОГДА** пользователь нажимает "Back"
- **ТОГДА** frontend ДОЛЖЕН вернуться на страницу pipeline detail

### Requirement: Маршрутизация
Frontend ДОЛЖЕН поддерживать прямой доступ к артефактам.

#### Scenario: Прямая ссылка
- **КОГДА** пользователь открывает `/artifacts/:path`
- **ТОГДА** Artifact Viewer ДОЛЖЕН загрузить и отобразить артефакт
