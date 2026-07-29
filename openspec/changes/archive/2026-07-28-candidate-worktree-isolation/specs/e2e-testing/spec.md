## ADDED Requirements

### Requirement: Реальный worktree delivery E2E

E2E MUST доказать, что agents/checks/delivery используют candidate worktree,
а live checkout сохраняет исходные branch, HEAD и source bytes.

#### Scenario: Candidate доставлен

- **КОГДА** mock-agent создаёт source, реальные checks проходят и delivery
  создаёт branch/push/PR
- **ТОГДА** remote MUST содержать candidate commit
- **И** live checkout MUST остаться неизменным
