## Purpose

Спецификация определяет нормативное поведение capability `agent-tester`.

## Requirements
### Requirement: Tester проверяет код на соответствие продуктовой спецификации
Агент Tester MUST прочитать продуктовую спецификацию и написать тесты. Controller
MUST запускать tests после этапа через typed check adapter.

#### Scenario: Tester создаёт отчёт тестирования
- **КОГДА** Tester запускается
- **ТОГДА** он MUST прочитать `.ai-team/artifacts/{feature}/specs/`
- **И** прочитать `.ai-team/artifacts/{feature}/review.md`
- **И** прочитать controller-owned identity reviewed candidate
- **И** написать интеграционные/E2E тесты для фичи
- **И** создать `.ai-team/artifacts/{feature}/test-report.md`

### Requirement: Содержимое отчёта тестирования
Отчёт test authoring MUST документировать, каким тестом покрывается каждый
критерий. Он MUST NOT выдавать выдуманный command output за execution evidence.

#### Scenario: Формат отчёта тестирования
- **КОГДА** отчёт тестирования создан
- **ТОГДА** он MUST содержать:
  - Список критериев приёмки из спецификации
  - test ID/scenario для каждого критерия
  - Известные ограничения покрытия
  - Общий authoring status: PASS / FAIL

### Requirement: Ворота пайплайна
Пайплайн MUST проверять отчёт тестирования перед переходом к Deployer.

#### Scenario: Тесты не прошли
- **КОГДА** Tester сообщает FAIL
- **ТОГДА** пайплайн MUST остановиться
- **И** НЕ переходить к Deployer

#### Scenario: Authoring PASS, typed check failed
- **КОГДА** Tester сообщает PASS, но required typed check завершается неуспешно или не обнаруживает tests
- **ТОГДА** pipeline MUST остановиться независимо от LLM-authored PASS
