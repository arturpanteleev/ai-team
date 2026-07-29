## 1. Identity

- [x] 1.1 Ввести Principal, canonical cloud roles и RBAC policies
- [x] 1.2 Реализовать bounded signed token issue/verify
- [x] 1.3 Добавить unit-тесты tampering, expiry и role validation

## 2. Web authentication

- [x] 2.1 Заменить общую session на per-browser session, связанную с principal
- [x] 2.2 Защитить cloud API reads, commands и WebSocket
- [x] 2.3 Брать actor identity решения только из trusted session
- [x] 2.4 Применить RBAC к start, resume, cancel и decision

## 3. UX и CLI

- [x] 3.1 Добавить bootstrap-команду выпуска token
- [x] 3.2 Добавить cloud login и отображение текущего principal в web
- [x] 3.3 Разрешать non-loopback bind только с authentication

## 4. Завершение

- [x] 4.1 Добавить backend/frontend integration tests
- [x] 4.2 Обновить русскую документацию
- [x] 4.3 Выполнить strict validation и `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
