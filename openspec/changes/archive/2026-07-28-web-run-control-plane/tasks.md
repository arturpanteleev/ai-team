## 1. Controller

- [x] 1.1 Реализовать async RunController start/resume/cancel
- [x] 1.2 Запретить дублирующий active worker и сохранить run identity
- [x] 1.3 Подключить exact approval store и listing

## 2. HTTP security и API

- [x] 2.1 Добавить session-cookie и CSRF middleware для write API
- [x] 2.2 Добавить POST runs, decisions, cancel и resume
- [x] 2.3 Добавить read API pending approvals и строгие JSON limits
- [x] 2.4 Покрыть unauthorized, stale, conflict и accepted tests

## 3. Frontend

- [x] 3.1 Получать session/CSRF и реализовать typed command client
- [x] 3.2 Добавить форму нового run и pending approval controls
- [x] 3.3 Показать actions, roles, exact subject, actor и history
- [x] 3.4 Обновлять control state по WebSocket с polling fallback

## 4. Завершение

- [x] 4.1 Добавить integration test browser/API → decision → resume
- [x] 4.2 Обновить русскую документацию
- [x] 4.3 Выполнить strict validation и `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
