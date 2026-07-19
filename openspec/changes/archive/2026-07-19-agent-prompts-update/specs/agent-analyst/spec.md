## MODIFIED Requirements

### Requirement: Промпт analyst
Промпт analyst ДОЛЖЕН содержать требования к структуре proposal.md и spec.md.

#### Scenario: Структура proposal.md
- **КОГДА** analyst создаёт proposal.md
- **ТОГДА** proposal.md ДОЛЖЕН содержать: бизнес-проблему, scope и out-of-scope, зафиксированные продуктовые требования, спорные моменты, Acceptance Criteria

#### Scenario: Структура AC
- **КОГДА** analyst создаёт Acceptance Criteria
- **ТОГДА** AC ДОЛЖНЫ описывать наблюдаемое поведение системы
- **И** включать: успешные сценарии, ошибки и невалидные данные, значимые edge cases, поведение, которое не должно измениться

#### Scenario: Формат spec.md
- **КОГДА** analyst создаёт spec.md
- **ТОГДА** spec.md ДОЛЖЕН быть в формате OpenSpec (markdown с заголовками ## ADDED Requirements)
