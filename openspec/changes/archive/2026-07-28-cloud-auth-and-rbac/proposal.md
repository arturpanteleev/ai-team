# Change: Облачная identity и RBAC

## Зачем

Локальная web-session защищает команды от CSRF, но не устанавливает личность
пользователя: клиент всё ещё может самостоятельно указать `actor_id` и роль.
Для облачного control plane это позволяет подменить автора решения и обойти
разделение обязанностей.

## Что меняется

- вводится проверяемая cloud identity с короткоживущим подписанным token;
- browser-session привязывается к аутентифицированному principal;
- решения используют actor identity и роли из server-side session, а не из
  недоверенного тела запроса;
- start, cancel и approval decisions получают явные RBAC-политики;
- локальный loopback-режим сохраняется без обязательной настройки cloud auth;
- web показывает текущего пользователя и не позволяет выбрать чужую роль.

## Вне scope

- внешний OIDC/SAML provider и управление организациями;
- billing, multi-tenant repository ACL и secret vault;
- worker isolation и распределённая очередь — это следующие changes.
