## Purpose

Спецификация определяет нормативное поведение capability `workflow-loopback`:
возврат к предыдущей стадии после негативного вердикта через approval-ребро
графа workflow.
## Requirements
### Requirement: Loopback при REJECTED
Возврат после `REJECTED` или `CHANGES_REQUESTED` MUST выполняться только после
persisted human decision и MUST NOT зависеть от наличия TTY.

#### Scenario: CHANGES_REQUESTED без TTY
- **КОГДА** reviewer возвращает `CHANGES_REQUESTED` и в графе есть
  rejected-ребро с approval policy (actions `return_to_coder`,
  `override_approve`, `reject`)
- **ТОГДА** pipeline MUST создать persisted approval с exact subject hash
- **И** decision `return_to_coder` MUST инвалидировать downstream attempts и
  продолжить тот же run с целевой стадией ребра

#### Scenario: Решение reject
- **КОГДА** уполномоченный человек выбирает `reject`
- **ТОГДА** run MUST завершиться как rejected без повторного запуска
  целевой стадии

### Requirement: Loopback задаётся рёбрами графа
Loopback MUST задаваться rejected-ребром графа workflow schema v4, цель
которого указывает на предыдущую стадию. Конфигурации агентов MUST NOT
содержать полей `loopback_to`, `max_retries`, `on_negative_verdict` —
маршрут, лимиты повторов (`max_visits`) и approvals живут только в
workflow edges/max_visits.

#### Scenario: Негативный вердикт без rejected-ребра
- **КОГДА** стадия возвращает негативный вердикт, а rejected-ребро в графе
  отсутствует
- **ТОГДА** pipeline MUST остановить run (fail-closed NegativeVerdictError)

#### Scenario: Исчерпан max_visits
- **КОГДА** счётчик визитов целевой стадии loopback достигает `max_visits`
- **ТОГДА** pipeline MUST остановить run с ошибкой лимита до запуска стадии
