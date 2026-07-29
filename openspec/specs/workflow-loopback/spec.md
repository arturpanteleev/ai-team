## Purpose

Спецификация определяет нормативное поведение capability `workflow-loopback`.
## Requirements
### Requirement: Loopback при REJECTED
Возврат после `REJECTED` или `CHANGES_REQUESTED` MUST выполняться только после
persisted human decision и MUST NOT зависеть от наличия TTY.

#### Scenario: CHANGES_REQUESTED без TTY
- **КОГДА** reviewer возвращает `CHANGES_REQUESTED` и retry доступен
- **ТОГДА** pipeline MUST создать approval с actions `return_to_coder`,
  `reject`, `request_information`, `override_approve`
- **И** decision `return_to_coder` MUST инвалидировать downstream attempts и
  продолжить тот же run с coder

#### Scenario: Решение reject
- **КОГДА** уполномоченный человек выбирает `reject`
- **ТОГДА** run MUST завершиться как rejected без запуска coder

### Requirement: max_retries в конфиге
Пайплайн MUST поддерживать поле `max_retries` для каждого агента.

#### Scenario: max_retries по умолчанию
- **КОГДА** `max_retries` не указан в конфигурации
- **ТОГДА** значение MUST быть 0 (без ретраев)

### Requirement: Default loopback target is metadata-driven
When a stage does not declare `loopback_to` explicitly, the system MUST
select the closest preceding stage whose definition declares
`mutation: source` as the default loopback target, rather than matching a
fixed name.

#### Scenario: Renamed source-writing stage
- **WHEN** a pipeline's source-writing stage is not named "coder" and no stage declares `loopback_to` explicitly
- **THEN** a negative verdict MUST still trigger loopback to that renamed stage, provided its definition declares `mutation: source`

#### Scenario: No eligible stage
- **WHEN** no preceding stage declares `mutation: source`
- **THEN** loopback MUST NOT trigger, matching the existing behavior for an unmatched explicit `loopback_to` target

