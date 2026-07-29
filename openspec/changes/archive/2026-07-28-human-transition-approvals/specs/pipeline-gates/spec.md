## MODIFIED Requirements

### Requirement: Единая checkpoint policy
Schema version 3 MUST нормализовать legacy checkpoints в persisted exact
transition approvals; TTY и flags MUST быть transport-ами общей модели.

#### Scenario: Checkpoint после этапа
- **КОГДА** защищённый transition достигнут после успешного stage
- **ТОГДА** pipeline MUST создать persisted approval с exact subject hash
- **И** MUST продолжить только после допустимого human decision

### Requirement: Неинтерактивный fail-closed режим
Checkpoint без TTY MUST сохранять pending approval и non-terminal waiting run,
а не терять возможность продолжения.

#### Scenario: Worker без решения
- **КОГДА** protected transition достигнут в non-interactive worker
- **ТОГДА** run MUST перейти в waiting и process MUST вернуть exit code 3
- **И** resume без resolved decision MUST быть отклонён
