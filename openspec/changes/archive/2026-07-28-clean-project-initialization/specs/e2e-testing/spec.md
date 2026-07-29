## MODIFIED Requirements

### Requirement: E2E-тест полного пайплайна
Система MUST иметь тест, проверяющий `ai-team run` от начала до конца, включая
первый запуск сразу после инициализации.

#### Scenario: Успешный pipeline после init
- **КОГДА** E2E-тест создаёт чистый Git project и запускает `ai-team init`
- **ТОГДА** `git status --porcelain` MUST остаться пустым
- **И** тест MUST запустить `ai-team run` без ручного изменения или коммита
  `.gitignore`
- **И** baseline guard MUST принять workspace
