## 1. Durable event API

- [x] 1.1 Добавить выборку events после глобального cursor
- [x] 1.2 Ввести versioned wire event и преобразование SQLite payload
- [x] 1.3 Покрыть ordering, pagination и malformed payload тестами

## 2. WebSocket production path

- [x] 2.1 Добавить SQLite tailer → WebSocket hub
- [x] 2.2 Реализовать replay по `?cursor=N` без окна потери
- [x] 2.3 Покрыть production bridge и reconnect/replay backend tests

## 3. Frontend

- [x] 3.1 Обновить TypeScript event contract
- [x] 3.2 Сохранять cursor, дедуплицировать и восстанавливать соединение
- [x] 3.3 Оставить редкий polling только как fallback
- [x] 3.4 Пересобрать embedded frontend

## 4. Завершение

- [x] 4.1 Строго провалидировать OpenSpec change
- [x] 4.2 Выполнить полный `make verify`
- [x] 4.3 Синхронизировать спецификации и архивировать change
