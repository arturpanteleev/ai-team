## ADDED Requirements

### Requirement: Проверка Acceptance Criteria
Verifier ДОЛЖЕН проверить каждый AC из proposal.md и сопоставить с фактическим результатом.

#### Scenario: Все AC пройдены
- **КОГДА** verifier проверяет AC из proposal.md
- **И** каждый AC соответствует наблюдаемому поведению системы
- **ТОГДА** verifier ДОЛЖЕН отметить AC как `✅ PASS`
- **И** общий вердикт: `APPROVED`

#### Scenario: Есть непройденные AC
- **КОГДА** verifier обнаруживает AC, который не соответствует поведению системы
- **ТОГДА** verifier ДОЛЖЕН отметить AC как `❌ FAIL`
- **И** описать расхождение
- **И** общий вердикт: `CHANGES_REQUESTED`

#### Scenario: AC не проверяем
- **КОГДА** AC невозможно проверить из-за ограничений окружения
- **ТОГДА** verifier ДОЛЖЕН отметить AC как `⚠️ NOT CHECKED`
- **И** указать причину пропуска

### Requirement: Self-review diff
Verifier ДОЛЖЕН выполнить self-review итогового diff.

#### Scenario: Проверка diff
- **КОГДА** verifier анализирует git diff
- **ТОГДА** verifier ДОЛЖЕН проверить:
  - соответствует ли diff proposal.md и spec.md
  - нет ли лишних изменений (unrelated refactoring)
  - нет ли очевидных багов
  - не пропущены ли важные edge cases
  - можно ли упростить решение без потери качества

### Requirement: Definition of Done checklist
Verifier ДОЛЖЕН проверить DoD checklist.

#### Scenario: DoD проверка
- **КОГДА** verifier выполняет verification pass
- **ТОГДА** verifier ДОЛЖЕН проверить:
  - выполнены ли Acceptance Criteria
  - реализация соответствует согласованному техническому решению
  - добавлены или обновлены необходимые тесты
  - проверены значимые edge cases и ошибки
  - не обнаружены непреднамеренные изменения поведения
  - соблюдён принцип минимального изменения
  - оценены риски security, performance и observability
  - известные ограничения явно перечислены

### Requirement: Формат verification.md
Verifier ДОЛЖЕН создать `verification.md` с результатами проверки.

#### Scenario: Структура verification.md
- **КОГДА** verifier завершает проверку
- **ТОГДА** `verification.md` ДОЛЖЕН содержать:
  - Общий вердикт: APPROVED / CHANGES_REQUESTED
  - Таблица AC: статус, описание расхождения (если есть)
  - Self-review результаты
  - DoD checklist с результатами
  - Известные ограничения
