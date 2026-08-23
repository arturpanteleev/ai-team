## Purpose

Единые fail-closed checkpoint policies для интерактивных и автоматизированных запусков.
## Requirements
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

### Requirement: Отдельное delivery approval
Обычный checkpoint approval MUST NOT разрешать commit, push или PR. Delivery
MUST требовать approval точного SHA-256 canonical plan.

#### Scenario: Gates подтверждены, delivery нет
- **КОГДА** передан `--approve-gates`, но не передан совпадающий `--approve-plan`
- **ТОГДА** delivery MUST остановиться после публикации плана и до внешних side effects

#### Scenario: Delivery approval является persisted approval
- **КОГДА** non-interactive run достигает подготовленного canonical delivery plan
- **ТОГДА** controller MUST создать persisted approval с `trigger: delivery_plan`,
  subject = SHA-256 canonical plan и ролью release_manager
- **И** canonical JSON плана MUST быть доступен в payload approval для web-решения
- **И** решение MUST приниматься через decision transport, либо `--resume` с
  совпадающим `--approve-plan`, который записывает exact-subject decision

### Requirement: Edge approval gate

Для schema v4 controller MUST создавать approval из policy выбранного edge,
а не из позиции узла в массиве.

#### Scenario: Approval action меняет target

- **КОГДА** resolved action указывает target, отличный от edge `to`
- **ТОГДА** lifecycle next stage MUST стать action target
- **И** решение и выбранное ребро MUST попасть в immutable evidence
