## 1. Runtime preflight

- [x] 1.1 Реализовать типизированный checker с timeout и безопасными сообщениями
- [x] 1.2 Проверять OpenCode, model/provider, credential allow-list и Git repository
- [x] 1.3 Проверять remote и `gh auth` только для workflow с delivery
- [x] 1.4 Включить preflight как обязательный gate непосредственно в controller start

## 2. Web API

- [x] 2.1 Добавить read endpoint preflight report
- [x] 2.2 Добавить безопасный bounded-tail endpoint immutable attempt log
- [x] 2.3 Покрыть traversal, truncation, отсутствующий run и failed readiness

## 3. Frontend

- [x] 3.1 Показать readiness и отдельные проверки до формы запуска
- [x] 3.2 Показать checks, mutations и delivery evidence этапа
- [x] 3.3 Показать live-лог раскрытого attempt с обновлением только во время выполнения
- [x] 3.4 Покрыть parsing, ошибки и UI frontend-тестами

## 4. Завершение

- [x] 4.1 Обновить русскую документацию
- [x] 4.2 Выполнить strict validation и `make verify`
- [x] 4.3 Синхронизировать спецификации и архивировать change
