## 1. Worktree lifecycle

- [x] 1.1 Реализовать create/load metadata exact candidate worktree
- [x] 1.2 Проверять clean baseline, repository identity, ancestry и symlink safety
- [x] 1.3 Восстанавливать тот же worktree при resume

## 2. Pipeline routing

- [x] 2.1 Разделить control target и candidate source root
- [x] 2.2 Направить agents, mutation guards и checks только в candidate
- [x] 2.3 Хранить artifacts в candidate и обновлять live control projection
- [x] 2.4 Направить planning/delivery в candidate без изменения live source

## 3. Candidate identity

- [x] 3.1 Сохранять base commit/tree, workspace/patch hash, files, checks и attempts
- [x] 3.2 Включить candidate hash в exact human approval subject
- [x] 3.3 Проверять одинаковую identity review/checks/delivery
- [x] 3.4 Инвалидировать downstream evidence после candidate mutation/loopback

## 4. Завершение

- [x] 4.1 Добавить unit, resume и реальный E2E worktree → push/PR
- [x] 4.2 Доказать тестом неизменность live checkout
- [x] 4.3 Обновить русскую документацию
- [x] 4.4 Выполнить strict validation и `make verify`
- [x] 4.5 Синхронизировать спецификации и архивировать change
