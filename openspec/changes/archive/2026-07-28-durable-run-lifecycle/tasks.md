## 1. Lifecycle state

- [x] 1.1 Реализовать versioned state model и atomic store
- [x] 1.2 Проверять identity, phases и допустимые state transitions
- [x] 1.3 Покрыть повреждение, atomicity и terminal rejection тестами

## 2. Evidence и engine

- [x] 2.1 Реализовать безопасное открытие существующего evidence chain для append
- [x] 2.2 Добавить `run_resumed` и deterministic replay
- [x] 2.3 Ввести RunEngine start/resume и stage-boundary checkpoints

## 3. CLI и projection

- [x] 3.1 Добавить `run --resume <run_id>` со строгой валидацией аргументов
- [x] 3.2 Возобновлять ту же SQLite run row и event sequence
- [x] 3.3 Добавить E2E restart/resume с неизменным run_id

## 4. Завершение

- [x] 4.1 Обновить русскую документацию
- [x] 4.2 Строго провалидировать change
- [x] 4.3 Выполнить полный `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
