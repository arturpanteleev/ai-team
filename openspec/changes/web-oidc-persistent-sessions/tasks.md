## Implementation

- [x] Таблицы `web_sessions` и `oidc_login_states`, схема v10.
- [x] Сессии в базе: TTL, скользящее продление, абсолютный срок, отзыв при выходе.
- [x] Пакет `pkg/oidc`: discovery, PKCE, проверка `id_token` по JWKS.
- [x] Маршруты `/api/auth/oidc/login` и `/api/auth/oidc/callback`, функции из реестра команды.
- [x] Флаги `ai-team web`, секрет клиента из окружения.
- [x] Тесты на заглушке провайдера: успех, state/nonce, подпись, срок, audience/issuer, сбой JWKS, отзыв, перезапуск, CSRF.
- [x] Документация `docs/reference/cli.md`, `docs/reference/security.md`.
