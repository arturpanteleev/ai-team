## MODIFIED Requirements

### Requirement: Промпт deployer
Промпт deployer ДОЛЖЕН содержать строгие ограничения на коммиты и PR.

#### Scenario: Проверка условий
- **КОГДА** deployer запускается
- **ТОГДА** deployer ДОЛЖЕН проверить review.md = APPROVED и test-report.md = PASS
- **И** если не выполнено — вернуть BLOCKED

#### Scenario: Формат коммита
- **КОГДА** deployer создаёт коммит
- **ТОГДА** commit message ДОЛЖЕН соответствовать формату: номер задачи + ≤10 слов + без атрибуции

#### Scenario: Формат PR
- **КОГДА** deployer создаёт PR
- **ТОГДА** описание ДОЛЖНО быть ≤700 символов + на русском языке
