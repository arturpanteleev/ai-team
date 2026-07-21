## ДОБАВЛЕННЫЕ Требования

### Requirement: Пакет pkg/eval
Система MUST предоставлять пакет для оценки качества артефактов.

#### Scenario: Eval структура
- **КОГДА** создаётся `pkg/eval`
- **ТОГДА** он MUST содержать тип `Eval` с полями AgentName, ArtifactPath, Criteria
- **И** тип `Result` с полями Score, Comment

#### Scenario: Запуск оценки
- **КОГДА** вызывается `eval.Run()`
- **ТОГДА** он MUST прочитать артефакт
- **И** запустить opencode с промптом-судьёй
- **И** распарсить оценку и комментарий

### Requirement: Команда ai-team eval
CLI MUST поддерживать команду `eval`.

#### Scenario: Eval запуск
- **КОГДА** пользователь запускает `ai-team eval --feature "hello-api" --task "..." --agent analyst`
- **ТОГДА** система MUSTА запустить Analyst, затем оценить его output
- **И** вывести таблицу с результатами

#### Scenario: Eval без пайплайна
- **КОГДА** пользователь запускает `ai-team eval --artifact reviews/hello-api/review.md`
- **ТОГДА** система MUSTА оценить только указанный артефакт без запуска пайплайна
