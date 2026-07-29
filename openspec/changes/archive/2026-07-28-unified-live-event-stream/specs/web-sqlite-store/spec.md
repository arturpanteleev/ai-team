## MODIFIED Requirements

### Requirement: Run and attempt projection
SQLite MUST хранить run/attempt projection и упорядоченный durable lifecycle
event stream.

#### Scenario: Cursor query
- **КОГДА** consumer запрашивает events после cursor с bounded limit
- **ТОГДА** store MUST вернуть только events с большим id
- **И** MUST упорядочить их по id по возрастанию
