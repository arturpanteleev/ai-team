## Purpose

Спецификация определяет нормативное поведение capability `e2e-testing`.

## Requirements
### Requirement: E2E-тест полного пайплайна
Система MUST иметь тест, проверяющий `ai-team run` от начала до конца.

#### Scenario: Успешный пайплайн
- **КОГДА** E2E-тест запускается
- **ТОГДА** он создаёт временный проект
- **И** запускает `ai-team init`
- **И** первый запуск с `--approve-gates` останавливается до side effects и публикует canonical plan SHA-256
- **И** второй запуск использует `--retry-from deployer --approve-plan <точный-sha256>`
- **И** проверяет proposal.md, specs/, design.md, tasks.md, review.md, test-report.md, verification.md и delivery-plan.json
- **И** проверяет immutable run evidence, successful required check и exact-file delivery commit
- **И** проверяет exit code первого запуска == 3 и второго == 0

#### Scenario: Пайплайн падает при REJECTED review
- **КОГДА** mock opencode для Reviewer возвращает CHANGES_REQUESTED
- **ТОГДА** пайплайн MUST остановиться на Reviewer
- **И** exit code MUST быть != 0

#### Scenario: Tester FAIL
- **КОГДА** mock opencode возвращает FAIL только на tester
- **ТОГДА** verifier и delivery MUST NOT выполняться
- **И** exit code MUST быть 1

#### Scenario: BLOCKED
- **КОГДА** mock opencode возвращает BLOCKED только на analyst
- **ТОГДА** pipeline MUST сохранить причину и завершиться с exit code 2

#### Scenario: Init проверяет структуру
- **КОГДА** `ai-team init` запускается
- **ТОГДА** E2E-тест проверяет, что созданы `.ai-team/config.yaml` и все директории артефактов
