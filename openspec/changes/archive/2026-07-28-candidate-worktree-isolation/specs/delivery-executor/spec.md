## ADDED Requirements

### Requirement: Delivery только из candidate

Planner и executor MUST строить, проверять, коммитить и отправлять exact
candidate worktree.

#### Scenario: Успешная delivery

- **КОГДА** plan hash одобрен и candidate identity неизменна
- **ТОГДА** executor MUST commit/push candidate branch
- **И** MUST NOT переключать live checkout
