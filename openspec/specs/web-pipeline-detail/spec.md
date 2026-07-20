## Purpose

Pipeline detail — страница с деталями pipeline run и его stages.

## Requirements

### Requirement: Pipeline detail page
Frontend MUST отображать детали pipeline run.

#### Scenario: Отображение этапов
- **КОГДА** пользователь открывает `/pipelines/:id`
- **ТОГДА** страница MUST показать:
  - Header: run_id, feature name, domain run status, started_at, duration
  - Список immutable attempts: attempt_id, stage index, status, execution, decision, outcome, verdict, duration, inputs/outputs

#### Scenario: Live updates
- **КОГДА** WebSocket отправляет stage_started/stage_completed или polling
  обнаруживает новую SQLite projection активного run
- **ТОГДА** страница MUST обновить статус этапа без browser reload

#### Scenario: Артефакты этапа
- **КОГДА** пользователь раскрывает этап
- **ТОГДА** страница MUST показать список артефактов (inputs/outputs)
- **И** каждый артефакт — кликабельная ссылка на Artifact Viewer
- **И** ссылка MUST содержать run_id и immutable evidence path

### Requirement: Status badges
Frontend MUST отображать цветовые badge для статусов.

#### Scenario: Цвета статусов
- **КОГДА** этап имеет статус
- **ТОГДА** badge MUST быть:
  - running — синий
  - completed — зелёный
  - failed — красный
  - blocked — оранжевый
  - skipped/interrupted/invalidated — отдельный неположительный стиль
