## Purpose

Спецификация определяет нормативное поведение capability `workflow-rollback`:
возобновление и повторное исполнение этапов через persisted resume и
approval-рёбра графа workflow.

## Requirements
### Requirement: Resume persisted run
CLI MUST поддерживать `--resume <run_id>` для продолжения non-terminal run
с сохранением identity, lineage и pending approvals.

#### Scenario: Resume без нового task
- **КОГДА** указан `--resume <run_id>`
- **ТОГДА** task, feature и next stage MUST быть загружены из persisted state
- **И** передача нового `--task` или `--feature` MUST быть отклонена

#### Scenario: Resume terminal run
- **КОГДА** указанный run уже terminal
- **ТОГДА** CLI MUST завершиться ошибкой без мутации evidence

### Requirement: Повторный запуск этапа через loopback/resume
Повторное исполнение завершённого этапа MUST выполняться через rejected-ребро
графа с human decision либо через resume не-terminal run; отдельного флага
пропуска этапов в новом run НЕ существует.

#### Scenario: Retry delivery после обрыва
- **КОГДА** deployer завершился с ошибкой после частичного delivery,
  и пользователь возобновляет тот же run через `--resume`
- **ТОГДА** пайплайн MUST проверить preconditions и ранее подготовленный
  delivery plan/state
- **И** MUST возобновить только незавершённые controller-owned delivery steps
- **И** MUST NOT создавать дублирующий commit или повторный PR
- **И** immutable evidence прежней попытки MUST NOT перезаписываться

#### Scenario: BLOCKED этап
- **КОГДА** этап сигнализирует BLOCKED и run остановлен
- **КОГДА** пользователь исправляет задачу и запускает новый run той же фичи
- **ТОГДА** CLI MUST предложить перезапуск задачи; артефакты прежних попыток
  остаются в immutable evidence
