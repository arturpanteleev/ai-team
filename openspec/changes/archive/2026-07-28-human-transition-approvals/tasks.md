## 1. Approval domain

- [x] 1.1 Реализовать typed PendingApproval/ApprovalDecision и atomic store
- [x] 1.2 Реализовать roles, actions, subject validation и any/all quorum
- [x] 1.3 Покрыть stale subject, duplicate actor, conflict и corruption tests

## 2. Pipeline semantics

- [x] 2.1 Создавать exact approval для role-protected перехода
- [x] 2.2 Сохранять lifecycle waiting и resume без повтора завершённого stage
- [x] 2.3 Перевести TTY и `--approve-gates` на общий decision path
- [x] 2.4 Реализовать human-approved review→coder loopback

## 3. Transport и evidence

- [x] 3.1 Добавить CLI-команду записи решения с actor/role/action/comment/hash
- [x] 3.2 Добавить approval_requested/approval_decided в immutable и SQLite events
- [x] 3.3 Добавить default approval roles и строгую config validation

## 4. Проверки и завершение

- [x] 4.1 Добавить unit/E2E non-TTY wait→decision→resume и review loopback
- [x] 4.2 Обновить русскую документацию
- [x] 4.3 Строго провалидировать change и выполнить `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
