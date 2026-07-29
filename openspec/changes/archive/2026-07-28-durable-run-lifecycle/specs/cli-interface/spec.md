## MODIFIED Requirements

### Requirement: Run identity and validation
`run` MUST валидировать новый запуск или resume до запуска агента, получить
workspace lock и использовать одну immutable run_id на весь долговечный run.

#### Scenario: Resume
- **КОГДА** пользователь запускает `ai-team run --resume <run_id>`
- **ТОГДА** CLI MUST восстановить feature, task и next stage
- **И** MUST продолжить ту же run identity после fail-closed проверок
