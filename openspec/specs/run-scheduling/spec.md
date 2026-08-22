# run-scheduling Specification

## Purpose
Persistent queue для distributed-исполнения: lease/claim/renew/complete с атомарными conditional updates и concurrency limits.
## Requirements
### Requirement: Persistent idempotent queue

Control plane MUST сохранять worker jobs до подтверждения HTTP command и MUST
не создавать два active job для одинаковых run ID и operation.

#### Scenario: Restart

- **КОГДА** control plane перезапущен после enqueue и до claim
- **ТОГДА** queued job MUST остаться доступным worker

#### Scenario: Duplicate resume

- **КОГДА** одинаковый resume enqueue-ится повторно пока первый queued/running
- **ТОГДА** scheduler MUST отклонить duplicate

### Requirement: Exclusive bounded lease

Claimed job MUST иметь worker owner, случайный lease token и expiry.

#### Scenario: Stale completion

- **КОГДА** старый worker пытается renew/complete job после re-claim
- **ТОГДА** операция MUST быть отклонена без изменения нового lease

#### Scenario: Worker потерян

- **КОГДА** lease истёк без heartbeat
- **ТОГДА** job MUST снова стать доступным для claim

### Requirement: Concurrency и target lock

Scheduler MUST атомарно применять configured global concurrency и не
исполнять два job одного target одновременно.

#### Scenario: Два worker

- **КОГДА** два worker одновременно claim-ят jobs одного repository target
- **ТОГДА** только один job MAY стать running

### Requirement: Distributed cancel

Cancel MUST сохраняться в queue и быть видимым worker через heartbeat.

#### Scenario: Running job

- **КОГДА** human с полномочием отменяет running run
- **ТОГДА** heartbeat MUST отменить disposable process
- **И** stale worker MUST NOT отметить job успешным
