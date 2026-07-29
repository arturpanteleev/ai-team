## MODIFIED Requirements

### Requirement: Флаг --retry-from
CLI MUST поддерживать `--retry-from <agent>` для нового run и
`--resume <run_id>` для продолжения persisted non-terminal run.

#### Scenario: Resume без нового task
- **КОГДА** указан `--resume <run_id>`
- **ТОГДА** task, feature и next stage MUST быть загружены из persisted state
- **И** передача нового `--task` MUST быть отклонена

#### Scenario: Resume terminal run
- **КОГДА** указанный run уже terminal
- **ТОГДА** CLI MUST завершиться ошибкой без мутации evidence
