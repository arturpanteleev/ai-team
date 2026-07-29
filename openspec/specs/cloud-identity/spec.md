# cloud-identity Specification

## Purpose
TBD - created by archiving change cloud-auth-and-rbac. Update Purpose after archive.
## Requirements
### Requirement: Проверяемая cloud identity

Cloud control plane MUST устанавливать actor identity только из
криптографически проверенного и неистёкшего credential.

#### Scenario: Валидный token

- **КОГДА** token имеет допустимую подпись, actor ID, роли и срок действия
- **ТОГДА** server MUST создать session для exact principal

#### Scenario: Подмена или expiry

- **КОГДА** token изменён, истёк или содержит неизвестную роль
- **ТОГДА** authentication MUST завершиться отказом без создания session

### Requirement: Server-side browser session

Cloud browser session MUST быть уникальной, ограниченной по времени и
привязанной к одному principal.

#### Scenario: Authenticated API и WebSocket

- **КОГДА** cloud mode включён
- **ТОГДА** API reads, commands и WebSocket MUST требовать валидную session
- **И** write command MUST дополнительно требовать session-bound CSRF token

### Requirement: Совместимый local mode

Loopback local mode MUST оставаться доступным без cloud credential.

#### Scenario: Локальный запуск

- **КОГДА** authentication явно не настроена и server bind-ится на loopback
- **ТОГДА** browser MUST получить local session без Bearer token
