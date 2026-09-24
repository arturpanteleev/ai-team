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

### Requirement: Local operator identity

Loopback local mode MUST оставаться доступным без cloud credential, но MUST
NOT работать без identity. Процесс web-сервера MUST выпускать локальный
operator token на время своей жизни, сообщать его оператору и принимать
session только в обмен на этот token.

#### Scenario: Локальный запуск

- **КОГДА** cloud authentication не настроена и server bind-ится на loopback
- **ТОГДА** процесс MUST выпустить локальный operator token и напечатать его
  вместе с URL входа
- **И** browser MUST получить session только предъявив этот token

#### Scenario: Запрос без operator token

- **КОГДА** клиент на loopback запрашивает session без предъявления token
- **ТОГДА** server MUST отказать и MUST NOT создать session

#### Scenario: Полномочия локального оператора

- **КОГДА** session создана по локальному operator token
- **ТОГДА** principal MUST иметь фиксированный actor ID и все канонические
  роли, а не роли, объявленные вызывающим
