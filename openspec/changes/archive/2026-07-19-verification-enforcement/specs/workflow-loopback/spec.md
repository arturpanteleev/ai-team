## MODIFIED Requirements

### Requirement: Loopback при REJECTED
Система MUST поддерживать возврат к coder-у при негативном вердикте reviewer-подобного агента; вердикт MUST читаться из фактических выходных артефактов этапа (см. verdict-contract), а не из фиксированного пути.

#### Scenario: REJECTED → coder
- **КОГДА** reviewer завершается и review.md содержит `**Verdict:** REJECTED` или `**Verdict:** CHANGES_REQUESTED`
- **И** `max_retries` цели loopback (coder) > 0 и лимит не исчерпан
- **И** stdin — терминал
- **ТОГДА** система MUST предложить: `{agent}: retry N/M [Y/n/diff]`
- **И** ЕСЛИ пользователь отвечает `Y` — перезапустить пайплайн с coder-а

#### Scenario: Review.md во входе при retry
- **КОГДА** coder перезапускается по loopback
- **ТОГДА** выходные артефакты вердикт-агента (review.md) MUST быть добавлены во входы coder-а
- **И** промпт coder-а MUST содержать их содержимое

#### Scenario: Лимит retries исчерпан
- **КОГДА** негативный вердикт получен, а количество retries достигло `max_retries`
- **ТОГДА** пайплайн MUST остановиться с ошибкой `Превышен лимит retries ({N}) для coder`
- **И** MUST NOT продолжать к следующему агенту

#### Scenario: Неинтерактивный режим
- **КОГДА** stdin — не терминал
- **ТОГДА** loopback MUST NOT предлагаться, действует verdict-enforcement (остановка)

### Requirement: max_retries в конфиге
Пайплайн MUST поддерживать поле `max_retries` в конфигурации агента-цели loopback (coder), а также поле `loopback_to` у вердикт-агента для явного указания цели.

#### Scenario: max_retries по умолчанию
- **КОГДА** `max_retries` не указан у цели loopback
- **ТОГДА** значение MUST быть 0 (loopback выключен)

#### Scenario: Цель loopback по умолчанию
- **КОГДА** `loopback_to` не указан у вердикт-агента
- **ТОГДА** целью MUST считаться ближайший предшествующий агент с именем `coder`
