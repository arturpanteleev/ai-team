## Purpose

Версионированная, строгая конфигурация ролей, checkpoints и детерминированных проверок.

## Requirements

### Requirement: Strict schema
Config loader MUST отклонять неизвестные и дублирующиеся поля, дополнительные YAML documents, повторяющиеся этапы и неизвестные ссылки loopback.

#### Scenario: Опечатка поля
- **КОГДА** config содержит `gate_afer`
- **ТОГДА** загрузка MUST завершиться ошибкой до создания run

### Requirement: Ролевые overrides
Каждый pipeline item MUST поддерживать `name`, `model`, `effort`, `cli`, `timeout`, `max_retries`, `loopback_to`, `on_negative_verdict`, checkpoint fields и `checks`.

#### Scenario: Global fallback
- **КОГДА** model, effort или cli не задан на этапе
- **ТОГДА** MUST использоваться соответствующее глобальное значение

#### Scenario: Effort
- **КОГДА** effort равен low, medium или high
- **ТОГДА** runtime MUST передать это значение в служебные требования prompt
- **И** неизвестное значение MUST быть отклонено

### Requirement: Deterministic checks config
Каждый check MUST задавать уникальное имя, class, argv-массив command, policy required или optional, а также MAY задавать timeout и confined working_dir.

#### Scenario: Shell-строка вместо argv
- **КОГДА** command не является непустым YAML-массивом
- **ТОГДА** config MUST быть отклонён

### Requirement: Обратная совместимость
Отсутствующий `schema_version` MUST интерпретироваться как legacy version 1; новые конфиги MUST сериализоваться с текущей schema version.

#### Scenario: Pipeline как массив строк
- **КОГДА** legacy config содержит `pipeline: [analyst, coder]`
- **ТОГДА** строки MUST быть нормализованы в AgentConfig с глобальными fallback values
