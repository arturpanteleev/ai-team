## ADDED Requirements

### Requirement: BLOCKED статус в StageResult
StageResult MUST поддерживать статус `blocked` помимо `passed` и `failed`.

#### Scenario: Агент возвращает BLOCKED
- **КОГДА** агент завершился со статусом `blocked`
- **ТОГДА** pipeline MUST остановиться
- **И** MUST NOT продолжать к следующему агенту
- **И** MUST показать сообщение: `Блокер: {описание блокера}`

### Requirement: Остановка pipeline при BLOCKED
Pipeline MUST останавливаться при получении BLOCKED статуса от любого агента.

#### Scenario: BLOCKED от analyst
- **КОГДА** analyst вернул BLOCKED (требования противоречивы)
- **ТОГДА** pipeline MUST остановиться
- **И** показать: `Требования неоднозначны: {детали}`
- **И** предложить: `Вернуться на этап анализа? Используйте: ai-team run --retry-from analyst`

#### Scenario: BLOCKED от architect
- **КОГДА** architect вернул BLOCKED (технически нереализуемо)
- **ТОГДА** pipeline MUST остановиться
- **И** показать: `Техническое решение невозможно: {детали}`
- **И** предложить: `Вернуться на этап проектирования? Используйте: ai-team run --retry-from architect`

### Requirement: Предложение retry-from при BLOCKED
Pipeline MUST предлагать retry-from агента, вернувшего BLOCKED.

#### Scenario: Предложение retry
- **КОГДА** агент вернул BLOCKED
- **ТОГДА** pipeline MUST вывести: `Для исправления запустите: ai-team run --retry-from {agent_name}`
- **И** pipeline MUST завершиться с кодом выхода, указывающим на BLOCKED

### Requirement: BLOCKED vs failed
Система MUST различать BLOCKED и failed статусы.

#### Scenario: BLOCKED — требует вмешательства
- **КОГДА** агент вернул BLOCKED
- **ТОГДА** pipeline MUST считать это "остановлено пользователем" (код выхода 0)
- **И** MUST NOT считать это ошибкой (код выхода != 1)

#### Scenario: failed — ошибка
- **КОГДА** агент вернул failed
- **ТОГДА** pipeline MUST считать это ошибкой (код выхода 1)
- **И** MUST NOT предлагать retry-from автоматически

### Requirement: Неинтерактивный режим для BLOCKED
Если stdin не является терминалом, BLOCKED MUST работать как failed.

#### Scenario: CI/pipe режим
- **КОГДА** `ai-team run` запущен с перенаправленным stdin (pipe)
- **И** агент вернул BLOCKED
- **ТОГДА** pipeline MUST завершиться с кодом выхода 1 (как failed)
- **И** MUST NOT ожидать ввод от пользователя
