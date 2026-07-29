## Purpose

Спецификация определяет нормативное поведение capability `workflow-rollback`.

## Requirements
### Requirement: Флаг --retry-from
CLI MUST поддерживать `--retry-from <agent>` для нового run и
`--resume <run_id>` для продолжения persisted non-terminal run.

#### Scenario: Перезапуск с указанного агента
- **КОГДА** пользователь запускает `ai-team run --retry-from coder`
- **ТОГДА** пайплайн MUST пропустить всех агентов до `coder` (включительно: analyst, architect — пропущены)
- **И** начать выполнение с агента `coder`
- **И** артефакты пропущенных этапов MUST NOT удаляться

#### Scenario: Resume без нового task
- **КОГДА** указан `--resume <run_id>`
- **ТОГДА** task, feature и next stage MUST быть загружены из persisted state
- **И** передача нового `--task` MUST быть отклонена

#### Scenario: Resume terminal run
- **КОГДА** указанный run уже terminal
- **ТОГДА** CLI MUST завершиться ошибкой без мутации evidence

### Requirement: Проверка входных артефактов при retry
Система MUST проверять наличие всех необходимых входных артефактов перед запуском с указанного этапа.

#### Scenario: Ошибка при отсутствии артефактов
- **КОГДА** пользователь запускает `--retry-from deployer`, но артефакты tester отсутствуют
- **ТОГДА** система MUST вывести ошибку: `missing artifacts from previous stage: tester`
- **И** MUST NOT запускать агента

### Requirement: Повторный запуск завершённого этапа
`--retry-from` MUST позволять перезапускать уже завершённый агент.

#### Scenario: Retry на последнем агенте
- **КОГДА** deployer завершился с ошибкой
- **И** пользователь запускает `ai-team run --retry-from deployer`
- **ТОГДА** пайплайн MUST проверить preconditions и ранее подготовленный delivery plan/state
- **И** MUST возобновить только незавершённые controller-owned delivery steps
- **И** MUST NOT создавать дублирующий commit или повторный PR
- **И** immutable evidence прежней попытки MUST NOT перезаписываться
