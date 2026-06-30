## ADDED Requirements

### Requirement: Флаг --retry-from для run
Команда `run` ДОЛЖНА поддерживать флаг `--retry-from` для перезапуска пайплайна с указанного агента.

#### Scenario: Retry-from с флагом
- **КОГДА** пользователь запускает `ai-team run --feature "add-auth" --retry-from coder`
- **ТОГДА** пайплайн ДОЛЖЕН запуститься с агента `coder`, пропустив analyst и architect
- **И** артефакты предыдущих этапов НЕ ДОЛЖНЫ перезаписываться

## MODIFIED Requirements

### Requirement: ai-team run
Система ДОЛЖНА выполнять пайплайн агентов для фичи.

#### Scenario: Запуск с флагом feature
- **КОГДА** пользователь запускает `ai-team run --feature "add-auth" --task "Реализовать JWT авторизацию"`
- **ТОГДА** система ДОЛЖНА создать `.ai-team/artifacts/tasks/add-auth/task.md` с описанием задачи
- **И** выполнить пайплайн: Analyst → Architect → Coder → Reviewer → Tester → Deployer
- **И** все артефакты (proposal, spec, design, review, test-report) сохраняются в `.ai-team/artifacts/add-auth/`
- **И** отчёты сохраняются в `.ai-team/reports/add-auth/`

#### Scenario: Остановка при ошибке агента
- **КОГДА** любой агент в пайплайне завершается с ошибкой
- **И** НЕ указан флаг `--retry-from`
- **ТОГДА** система ДОЛЖНА остановить выполнение
- **И** вывести сообщение об ошибке с именем упавшего агента и причиной
- **И** сгенерировать HTML-отчёт с указанием упавшего этапа
