## ADDED Requirements

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
