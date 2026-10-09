# cloud-identity Specification

## Purpose
Cloud auth: HMAC token с immutable actor ID и ролями, обмен на HttpOnly session; actor решения берётся с сервера.
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

### Requirement: Локальная аутентификация

Loopback local mode MUST выдавать случайный bearer token, если cloud
authentication не настроена. Token MUST быть сохранён в regular file без
symlink с правами `0600`; сервер MUST проверять его до создания browser session.
Пишущие запросы MUST требовать аутентифицированную session и CSRF token.

#### Scenario: Локальный запуск без cloud credential

- **КОГДА** authentication явно не настроена и server bind-ится на loopback
- **ТОГДА** CLI MUST напечатать локальный Bearer token и сохранить его в `.ai-team/web.token` с правами `0600`
- **И** `/api/session` MUST вернуть `401` без токена и создать session с trusted local principal с валидным токеном
- **И** пишущий запрос без токена MUST завершиться `401`
