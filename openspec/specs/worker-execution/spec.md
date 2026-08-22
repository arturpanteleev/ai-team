# worker-execution Specification

## Purpose
Disposable worker process исполняет ровно один strict job (start/resume/cancel) через общий RunEngine без альтернативной cloud-семантики.
## Requirements
### Requirement: Versioned worker job

Control plane MUST передавать disposable worker строгий bounded job document
с operation, run identity и exact mounted target.

#### Scenario: Невалидный job

- **КОГДА** job содержит неизвестное поле, несовместимые параметры или другой
  target
- **ТОГДА** worker MUST завершиться до mutation lifecycle/candidate

### Requirement: Один job на worker process

Worker process MUST исполнить ровно один start, resume или cancel job и после
этого завершиться.

#### Scenario: Start

- **КОГДА** worker получает валидный start job
- **ТОГДА** он MUST вызвать общий RunEngine с заданными run ID, feature и task

#### Scenario: Context cancellation

- **КОГДА** control plane отменяет выполняющийся worker
- **ТОГДА** launcher MUST остановить process
- **И** persisted cancel MUST выполняться отдельной идемпотентной операцией

### Requirement: Общая доменная модель

Worker MUST использовать те же lifecycle, candidate, approval, checks,
delivery и evidence contracts, что локальный RunEngine.

#### Scenario: Pending approval

- **КОГДА** disposable worker достигает защищённого перехода
- **ТОГДА** он MUST сохранить pending approval и завершить process
- **И** следующий resume job MUST продолжить тот же run и candidate

### Requirement: Infrastructure isolation

Cloud launcher MUST исполнять worker в disposable infrastructure isolation с
exact repository candidate mount и минимальными credentials.

#### Scenario: Reference subprocess

- **КОГДА** используется локальный reference launcher
- **ТОГДА** система MUST явно считать его development fallback, а не strict
  OS sandbox
