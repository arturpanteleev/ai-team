## ADDED Requirements

### Requirement: Разделение control и candidate root

Controller MUST хранить durable state в control target, но MUST выполнять
source mutation и checks только в exact candidate root.

#### Scenario: Agent пытается изменить live source

- **КОГДА** mutation отсутствует в candidate delta, но появляется в live source
- **ТОГДА** run MUST завершиться fail-closed
