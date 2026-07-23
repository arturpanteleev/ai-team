## Purpose

Спецификация определяет нормативное поведение capability `workflow-loopback`.
## Requirements
### Requirement: Loopback при REJECTED
Система MUST поддерживать возврат к coder-у при вердикте REJECTED или CHANGES_REQUESTED от reviewer-а.

#### Scenario: REJECTED → coder
- **КОГДА** reviewer завершается с вердиктом `**Verdict:** REJECTED`
- **И** `max_retries` для coder-а > 0
- **И** количество retries не превышено
- **ТОГДА** система MUST предложить: `Reviewer отклонил. Отправить обратно coder-у? [Y/n]`
- **И** ЕСЛИ пользователь отвечает `Y`, запустить coder снова с review.md как дополнительным входом

#### Scenario: CHANGES_REQUESTED → coder
- **КОГДА** reviewer завершается с `**Verdict:** CHANGES_REQUESTED`
- **ТОГДА** система MUST предложить отправить обратно coder-у аналогично REJECTED

#### Scenario: Лимит retries
- **КОГДА** количество retries превысило `max_retries`
- **ТОГДА** пайплайн MUST остановиться с ошибкой
- **И** сообщить: `Превышен лимит retries (N) для coder-а`

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

