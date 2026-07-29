## ADDED Requirements

### Requirement: Content-addressed immutable blobs

Persistent artifact storage MUST адресовать blob по SHA-256 содержимого и
проверять digest при чтении.

#### Scenario: Повторный blob

- **КОГДА** одинаковое содержимое архивируется повторно
- **ТОГДА** store MUST безопасно переиспользовать тот же object

#### Scenario: Corruption

- **КОГДА** сохранённые bytes не совпадают с запрошенным digest
- **ТОГДА** read/restore MUST завершиться ошибкой

### Requirement: Run artifact manifest

После worker execution scheduler MUST публиковать manifest всех regular files
immutable run evidence с relative path, digest и size.

#### Scenario: Unsafe evidence entry

- **КОГДА** run tree содержит symlink или non-regular file
- **ТОГДА** archive MUST быть отклонён

#### Scenario: Restore

- **КОГДА** manifest и blobs валидны
- **ТОГДА** system MUST восстановить exact relative files и повторно
  проверить digest каждого файла
