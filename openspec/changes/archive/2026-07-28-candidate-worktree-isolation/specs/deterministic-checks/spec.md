## ADDED Requirements

### Requirement: Checks исполняются на candidate

Каждый deterministic check MUST использовать candidate root и записывать
его workspace hash before/after.

#### Scenario: Проверка прошла

- **КОГДА** required check завершился успешно
- **ТОГДА** его before/after hash MUST совпадать с candidate identity
- **И** evidence MUST NOT описывать live checkout
