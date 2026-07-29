## ADDED Requirements

### Requirement: Approval subject привязан к candidate

Subject transition approval, относящегося к source workflow, MUST включать
exact candidate workspace hash.

#### Scenario: Решение устарело после mutation

- **КОГДА** candidate workspace hash больше не совпадает с hash subject
- **ТОГДА** decision или resume MUST быть отклонён как stale
