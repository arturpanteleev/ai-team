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

### Requirement: Системное право администратора отдельно от функции

Роль `admin` MUST быть системным правом, а не функцией процесса: шаблон MUST NOT
принимать её как `function` или `approver_function`, а `product_owner` MUST NOT
подразумевать её. Единственное permission роли — `gate:override` (обход
проваленного обязательного гейта). Создатель команды (первый администратор)
MUST получать `admin` вместе с `product_owner`; миграция существующей базы MUST
выдать `admin` только владельцу команды (актор записи `bootstrap`, иначе самый
ранний активный `product_owner`) и оставить журнал аудита, не выдавая право
остальным PO. В локальном режиме локальный владелец MUST получать `admin`
явно. Выдавать и снимать `admin` MAY только администратор, а последнего
активного администратора MUST NOT быть возможно снять или отозвать.

#### Scenario: Product owner не получает обход

- **КОГДА** участник с `product_owner` и всеми функциями процесса, но без `admin`,
  запрашивает обход проваленного гейта
- **ТОГДА** сервер MUST ответить `403`

#### Scenario: Миграция владельца

- **КОГДА** база без роли `admin` содержит двух активных product owner
- **ТОГДА** после миграции `admin` MUST иметь только владелец команды
