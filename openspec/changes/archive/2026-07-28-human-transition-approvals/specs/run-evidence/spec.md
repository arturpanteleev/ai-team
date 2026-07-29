## MODIFIED Requirements

### Requirement: Run and attempt identity
Run evidence MUST сохранять identity attempts, transitions и всех человеческих
approval requests/decisions в одной verified chain.

#### Scenario: Approval evidence
- **КОГДА** approval создаётся или получает решение
- **ТОГДА** event MUST содержать approval_id, subject hash, actor/action при
  решении и связанные from/to stages
