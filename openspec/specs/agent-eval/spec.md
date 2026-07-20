## Purpose

Спецификация определяет нормативное поведение capability `agent-eval`.

## Requirements
### Requirement: Пакет pkg/eval
Система MUST предоставлять пакет для оценки качества артефактов.

#### Scenario: Eval структура
- **КОГДА** создаётся `pkg/eval`
- **ТОГДА** он MUST содержать тип `Eval` с полями AgentName, ArtifactPath, Criteria
- **И** тип `Result` MUST содержать Score, Comment, layer, advisory, timestamps и raw evidence

#### Scenario: Запуск оценки
- **КОГДА** вызывается `eval.Run()`
- **ТОГДА** он MUST прочитать артефакт
- **И** запустить opencode с промптом-судьёй
- **И** потребовать ровно один score и один непустой comment
- **И** выполнить судью в изолированном временном каталоге

### Requirement: Статистическая LLM-оценка не является гейтом
LLM quality eval MUST быть advisory и MUST NOT переопределять детерминированные проверки.

#### Scenario: Несколько samples
- **КОГДА** пользователь задаёт `--samples` от 1 до 20
- **ТОГДА** система MUST сохранить все samples, median, mean и standard deviation в атомарно опубликованный JSON evidence

#### Scenario: Нестабильный судья
- **КОГДА** LLM score ниже ожидания или samples расходятся
- **ТОГДА** результат MUST оставаться advisory до появления калиброванного набора и явной политики

### Requirement: Команда ai-team eval
CLI MUST поддерживать команду `eval`.

#### Scenario: Eval запуск
- **КОГДА** пользователь запускает `ai-team eval --feature "hello-api" --task "..." --agent analyst`
- **ТОГДА** система MUST запустить только Analyst, затем оценить его фактические file outputs из definition
- **И** вывести таблицу с результатами
- **И** сохранить JSON evidence

#### Scenario: Eval без пайплайна
- **КОГДА** пользователь запускает `ai-team eval --artifact reviews/hello-api/review.md`
- **ТОГДА** система MUST оценить только указанный артефакт без запуска пайплайна
