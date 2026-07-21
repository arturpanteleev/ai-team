## Purpose

Спецификация определяет нормативное поведение capability `agent-reviewer`.

## Requirements
### Requirement: Reviewer проверяет код на соответствие спецификациям
Агент Reviewer MUST прочитать продуктовые спецификации и проверить реализацию.

#### Scenario: Reviewer создаёт отчёт ревью
- **КОГДА** Reviewer запускается
- **ТОГДА** он MUST прочитать `.ai-team/artifacts/{feature}/specs/`
- **И** прочитать controller-owned `.ai-team/artifacts/{feature}/.control/review-candidate.json`
- **И** проверить реализованный код в целевом проекте
- **И** создать `.ai-team/artifacts/{feature}/review.md`

### Requirement: Категории ревью
Ревью MUST категоризировать проблемы как APPROVED, CHANGES_REQUESTED или REJECTED.

#### Scenario: Вердикт ревью
- **КОГДА** ревью завершено
- **ТОГДА** review.md MUST содержать:
  - Общий вердикт: APPROVED / CHANGES_REQUESTED / REJECTED
  - Список проблем по серьёзности
  - Ссылки на требования спецификации и критерии приёмки
  - Замечания по качеству кода и безопасности

#### Scenario: Блокирующие проблемы
- **КОГДА** ревьюер находит блокирующие проблемы
- **ТОГДА** вердикт MUST быть REJECTED или CHANGES_REQUESTED
- **И** пайплайн MUST остановиться и сообщить о проблемах

### Requirement: Reviewer связан с candidate identity
Reviewer MUST получить exact workspace digest, changed paths, fingerprints и
tracked patch SHA-256 от controller.

#### Scenario: Candidate изменён после review
- **КОГДА** workspace digest больше не совпадает с reviewed candidate
- **ТОГДА** tester MUST NOT принимать прежний review как относящийся к новому candidate
