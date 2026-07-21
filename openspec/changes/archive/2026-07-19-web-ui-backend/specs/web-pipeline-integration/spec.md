## ADDED Requirements

### Requirement: WebNotifier
Система MUST реализовать Notifier для записи в SQLite и push через WebSocket.

#### Scenario: Notify stage completed
- **КОГДА** pipeline вызывает `notifier.Notify(stageResult)`
- **ТОГДА** WebNotifier MUST:
  - обновить запись stage в SQLite (статус, duration, error)
  - broadcast WebSocket event

#### Scenario: Pipeline started
- **КОГДА** pipeline запускается
- **ТОГДА** WebNotifier MUST создать запись в pipeline_runs

#### Scenario: Pipeline completed
- **КОГДА** pipeline завершается
- **ТОГДА** WebNotifier MUST обновить запись в pipeline_runs (status, completed_at)

### Requirement: Event emitter в pipeline
Pipeline MUST уведомлять web backend о stage_started events.

#### Scenario: Stage started callback
- **КОГДА** pipeline начинает выполнение агента
- **ТОГДА** pipeline MUST вызвать callback с событием stage_started
- **И** передать: pipeline_id, agent_name
