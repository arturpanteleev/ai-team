## MODIFIED Requirements

### Requirement: BLOCKED статус в StageResult
StageResult MUST поддерживать статус `blocked` помимо `passed` и `failed`; источником статуса является status-файл агента (см. verdict-contract).

#### Scenario: Агент сигнализирует BLOCKED
- **КОГДА** после завершения агента существует `.ai-team/artifacts/{feature}/status/{agent}.md` со строкой `**Status:** BLOCKED`
- **ТОГДА** этап MUST получить статус `blocked`, а поле Blocker — текст из `**Blocker:**`
- **И** пайплайн MUST остановиться, НЕ выполняя последующих агентов
- **И** показать сообщение `Блокер: {описание}`

### Requirement: Предложение retry-from при BLOCKED
Pipeline MUST предлагать retry-from агента, вернувшего BLOCKED.

#### Scenario: Предложение retry
- **КОГДА** агент вернул BLOCKED
- **ТОГДА** pipeline MUST вывести: `Для исправления уточните задачу и запустите: ai-team run --feature {feature} --retry-from {agent}`

### Requirement: BLOCKED vs failed
Система MUST различать BLOCKED и failed статусы через exit-код.

#### Scenario: Exit-код BLOCKED
- **КОГДА** пайплайн остановлен из-за BLOCKED
- **ТОГДА** exit-код MUST быть 2 — и в интерактивном, и в неинтерактивном режиме

#### Scenario: Exit-код failed
- **КОГДА** этап завершился ошибкой
- **ТОГДА** exit-код MUST быть 1

## REMOVED Requirements

### Requirement: Неинтерактивный режим для BLOCKED
**Причина**: противоречила требованию «код выхода, указывающий на BLOCKED» (blocked маскировался под failed). Заменена единым exit-кодом 2 в требовании «BLOCKED vs failed».
