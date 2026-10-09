## Purpose

Спецификация определяет controller-owned delivery без передачи внешних побочных эффектов LLM-агенту.

## Requirements

### Requirement: Детерминированный delivery plan
Контроллер MUST сформировать строгий JSON delivery plan только из файлов, изменение которых атрибутировано актуальным попыткам текущего run.

#### Scenario: В workspace есть чужое dirty-изменение
- **КОГДА** dirty-файл не изменялся ни одной актуальной попыткой текущего run
- **ТОГДА** контроллер MUST NOT включать этот файл в delivery plan

### Requirement: Настраиваемые предусловия delivery
Перед подготовкой плана контроллер MUST проверить все имена из `delivery.require_checks` PR-этапа реализации. Проверка считается выполненной только при controller-owned результате `passed` с политикой `required`, валидным digest результата и immutable attempt-manifest evidence для точного текущего workspace digest до и после проверки.

Этапы из `delivery.require_verdicts` являются дополнительными предусловиями. Для каждого указанного этапа контроллер MUST подтвердить успешную попытку, required verdict contract и положительный verdict по immutable output snapshot, связанный с тем же attempt manifest. План MUST сохранять digest этих snapshots. Отсутствие review/QA-артефактов не блокирует delivery, если они не указаны в конфигурации.

Canonical plan MUST фиксировать точный workspace digest и controller check evidence. Подтверждение человека MUST оставаться связанным с точным canonical plan hash и identity candidate.

LLM-вердикты или markers отчётов MUST NOT заменять controller-owned `require_checks`. Если обязательная проверка или настроенный verdict отсутствует, отрицателен, относится к другому workspace либо не подтверждается immutable evidence, контроллер MUST остановить попытку до подготовки плана, запроса approval и любых Git/GitHub side effects.

#### Scenario: Required checks разрешают delivery без review stages
- **КОГДА** каждый `delivery.require_checks` имеет controller-owned статус `passed` на точном текущем workspace digest
- **И** `delivery.require_verdicts` не задан
- **ТОГДА** контроллер MUST подготовить delivery plan без review, QA и verification артефактов

#### Scenario: Required check отсутствует или провален
- **КОГДА** named required check не запускался, имеет статус отличный от `passed`, не имеет валидного controller evidence либо проверял другой workspace digest
- **ТОГДА** контроллер MUST остановиться до подготовки плана и approval

#### Scenario: Дополнительный verdict отсутствует или отрицателен
- **КОГДА** этап из `delivery.require_verdicts` не завершился, не имеет требуемого verdict contract либо immutable output snapshot содержит отрицательный verdict
- **ТОГДА** контроллер MUST остановиться до подготовки плана и approval

#### Scenario: Результат runtime содержит одобрительные markers без controller checks
- **КОГДА** LLM-агент выводит `APPROVED` или `PASS`, но хотя бы один named required check не имеет controller-owned успешного evidence
- **ТОГДА** контроллер MUST запретить delivery

### Requirement: Controller-owned исполнение
Delivery MUST выполняться контроллером без запуска LLM runtime и без shell-интерполяции.

#### Scenario: Успешная доставка
- **КОГДА** все configured delivery preconditions выполнены
- **И** существует controller check evidence для точного workspace digest
- **И** точный план явно подтверждён человеком
- **ТОГДА** контроллер MUST создать или выбрать feature branch
- **И** MUST добавить в index только точные файлы плана
- **И** MUST создать commit, push и pull request

#### Scenario: Проверки не пройдены
- **КОГДА** любой configured precondition отсутствует либо отрицателен
- **ТОГДА** контроллер MUST запретить delivery до любых git/GitHub side effects

### Requirement: Возобновляемая доставка
Контроллер MUST сохранять plan hash, commit SHA, push и PR results для идемпотентного resume.

#### Scenario: PR creation упал после push
- **КОГДА** повторный запуск использует тот же plan hash и сохранённый HEAD
- **ТОГДА** контроллер MUST NOT создавать второй commit
- **И** MUST продолжить с незавершённого PR шага
