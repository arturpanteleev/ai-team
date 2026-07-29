## 1. Worker protocol

- [x] 1.1 Ввести строгий versioned Job для start/resume/cancel
- [x] 1.2 Валидировать exact target, run identity и несовместимые поля
- [x] 1.3 Добавить bounded diagnostics и unit-тесты protocol

## 2. Execution

- [x] 2.1 Реализовать process-backed RunEngine без shell
- [x] 2.2 Добавить `ai-team worker` для одного stdin job
- [x] 2.3 Подключить worker к existing config, candidate, evidence и recorder
- [x] 2.4 Добавить opt-in worker command в web control plane

## 3. Завершение

- [x] 3.1 Добавить integration test control plane → disposable process
- [x] 3.2 Обновить русскую документацию
- [x] 3.3 Выполнить strict validation и `make verify`
- [x] 3.4 Синхронизировать спецификации и архивировать change
