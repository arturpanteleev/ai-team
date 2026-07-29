# candidate-isolation Specification

## Purpose
TBD - created by archiving change candidate-worktree-isolation. Update Purpose after archive.
## Requirements
### Requirement: Отдельный candidate root на run

Каждый Git run MUST создать worktree от exact baseline commit до первого
source-mutating agent или deterministic check.

#### Scenario: Coder изменяет source

- **КОГДА** coder создаёт или меняет файл
- **ТОГДА** mutation MUST находиться только в candidate root
- **И** live source checkout MUST остаться неизменным

#### Scenario: Untracked live file

- **КОГДА** live checkout содержит untracked user file
- **ТОГДА** новый run MUST быть отклонён clean baseline guard
- **И** файл MUST NOT быть скопирован в candidate

### Requirement: Durable candidate recovery

Candidate metadata MUST связывать run, control target, worktree и base
commit/tree и восстанавливать тот же root после restart.

#### Scenario: Resume

- **КОГДА** waiting run возобновляется
- **ТОГДА** controller MUST открыть существующий worktree
- **И** MUST отклонить другой repository, baseline или unsafe path

### Requirement: Единая candidate identity

Candidate identity MUST содержать base commit/tree, workspace hash, patch
hash, changed files, checks и attempts.

#### Scenario: Candidate изменился после решения

- **КОГДА** source workspace hash изменился после human decision
- **ТОГДА** прежний subject MUST NOT разрешать новый transition

#### Scenario: Delivery identity отличается

- **КОГДА** delivery verification или plan ссылается на другой workspace hash
- **ТОГДА** commit, push и PR MUST быть запрещены

### Requirement: Нет неявной promotion

Agent execution и delivery MUST NOT менять branch, HEAD или source files live
checkout.

#### Scenario: Delivery завершилась

- **КОГДА** candidate commit отправлен и PR создан
- **ТОГДА** live checkout MUST остаться на исходных branch и HEAD
