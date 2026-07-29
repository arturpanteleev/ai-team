# Design: Облачная identity и RBAC

## Модель

`Principal` содержит immutable actor ID и набор нормализованных ролей:
Product Owner, Architect, Developer, Reviewer, QA и Release Manager. Ядро
авторизации не зависит от HTTP.

Встроенный token manager выпускает и проверяет versioned HMAC-SHA256 token с
`issued_at` и `expires_at`. Секрет не хранится в репозитории и передаётся
через окружение. Token используется только для создания browser-session;
после этого HttpOnly cookie и отдельный CSRF token привязаны к exact
principal и имеют ограниченный срок жизни.

## Web integration

В local loopback mode session сохраняет прежнюю ergonomics. В cloud mode
`/api/session` требует Bearer token. Все API reads, write commands и
WebSocket требуют связанную session. Решение получает `actor_id` из session,
а выбранная роль должна входить в роли principal и required roles approval.

RBAC:

- start: Product Owner или Architect;
- resume: любая аутентифицированная workflow-роль, дальнейший transition всё
  равно защищён exact approval;
- cancel: Product Owner или Release Manager;
- decision: роль principal должна совпадать с выбранной required role.

CLI-команда выпуска token только при наличии server secret. Это bootstrap для
локального/self-hosted развёртывания; внешний IdP позже сможет реализовать тот
же verifier contract.

## Безопасность

- подпись сравнивается constant-time;
- token имеет bounded lifetime и строгий JSON contract;
- server-side session не принимает actor identity из клиента;
- non-loopback bind разрешён только при включённой authentication;
- auth failures не раскрывают секрет или содержимое подписи.
