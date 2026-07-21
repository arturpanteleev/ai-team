## ADDED Requirements

### Requirement: Pipeline detail page
Frontend MUST отображать детали pipeline run.

#### Scenario: Отображение этапов
- **КОГДА** пользователь открывает `/pipelines/:id`
- **ТОГДА** страница MUST показать:
  - Header: feature name, overall status, started_at, duration
  - Список этапов: agent name, status badge, duration, inputs/outputs

#### Scenario: Live updates
- **КОГДА** WebSocket отправляет stage_started/stage_completed
- **ТОГДА** страница MUST обновить статус этапа без перезагрузки

#### Scenario: Артефакты этапа
- **КОГДА** пользователь раскрывает этап
- **ТОГДА** страница MUST показать список артефактов (inputs/outputs)
- **И** каждый артефакт — кликабельная ссылка на Artifact Viewer

### Requirement: Status badges
Frontend MUST отображать цветовые badge для статусов.

#### Scenario: Цвета статусов
- **КОГДА** этап имеет статус
- **ТОГДА** badge MUST быть:
  - running — синий
  - passed — зелёный
  - failed — красный
  - blocked — оранжевый
