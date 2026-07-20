## Purpose

Спецификация определяет controller-owned delivery без передачи внешних побочных эффектов LLM-агенту.

## Requirements

### Requirement: Детерминированный delivery plan
Контроллер MUST сформировать строгий JSON delivery plan только из файлов, изменение которых атрибутировано актуальным попыткам текущего run.

#### Scenario: В workspace есть чужое dirty-изменение
- **КОГДА** dirty-файл не изменялся ни одной актуальной попыткой текущего run
- **ТОГДА** контроллер MUST NOT включать этот файл в delivery plan

### Requirement: Controller-owned исполнение
Delivery MUST выполняться контроллером без запуска LLM runtime и без shell-интерполяции.

#### Scenario: Успешная доставка
- **КОГДА** review = APPROVED, test-report = PASS и verification = APPROVED
- **И** существует успешный required check класса unit, integration или e2e
- **И** точный план явно подтверждён
- **ТОГДА** контроллер MUST создать или выбрать feature branch
- **И** MUST добавить в index только точные файлы плана
- **И** MUST создать commit, push и pull request

#### Scenario: Проверки не пройдены
- **КОГДА** любой precondition или required check отсутствует либо отрицателен
- **ТОГДА** контроллер MUST запретить delivery до любых git/GitHub side effects

### Requirement: Возобновляемая доставка
Контроллер MUST сохранять plan hash, commit SHA, push и PR results для идемпотентного resume.

#### Scenario: PR creation упал после push
- **КОГДА** повторный запуск использует тот же plan hash и сохранённый HEAD
- **ТОГДА** контроллер MUST NOT создавать второй commit
- **И** MUST продолжить с незавершённого PR шага
