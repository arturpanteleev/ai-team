## MODIFIED Requirements

### Requirement: Формат verification.md
Verifier MUST создать `verification.md` с результатами проверки и вердиктом в каноническом формате verdict-контракта.

#### Scenario: Структура verification.md
- **КОГДА** verifier завершает проверку
- **ТОГДА** `verification.md` MUST содержать:
  - строку вердикта в каноническом формате: `**Verdict:** APPROVED` или `**Verdict:** CHANGES_REQUESTED`
  - Таблицу AC: статус, описание расхождения (если есть)
  - Self-review результаты
  - DoD checklist с результатами
  - Известные ограничения

#### Scenario: Вердикт распознаётся харнессом
- **КОГДА** пайплайн завершил этап verifier
- **ТОГДА** вердикт из verification.md MUST быть распознан парсером verdict-контракта
- **И** при CHANGES_REQUESTED к verifier применяется verdict-enforcement
