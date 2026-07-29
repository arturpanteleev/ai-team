## ADDED Requirements

### Requirement: Безопасный просмотр immutable логов

Web API MUST отдавать только bounded tail лога exact attempt внутри
immutable run directory.

#### Scenario: Запрошен большой лог

- **КОГДА** лог превышает максимальный размер ответа
- **ТОГДА** API MUST вернуть только хвост
- **И** MUST сообщить byte offset и признак усечения

#### Scenario: Попытка traversal

- **КОГДА** run или attempt identity содержит path traversal
- **ТОГДА** API MUST отклонить запрос без чтения файла вне run directory
