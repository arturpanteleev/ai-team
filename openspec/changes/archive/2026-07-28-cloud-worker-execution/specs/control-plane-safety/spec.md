## ADDED Requirements

### Requirement: Control plane не исполняет cloud run inline

При настроенном cloud worker launcher control plane MUST передавать execution
отдельному worker process/job и MUST NOT вызывать AI runtime в HTTP process.

#### Scenario: Worker command настроен

- **КОГДА** web control plane получает start или resume
- **ТОГДА** он MUST создать worker job через process-backed engine
- **И** HTTP process MUST только наблюдать persisted lifecycle/events
