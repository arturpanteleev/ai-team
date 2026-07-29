## MODIFIED Requirements

### Requirement: Explicit approvals
CLI MUST создавать и применять typed exact-subject decisions. Обычный
transition approval MUST NOT разрешать delivery side effects.

#### Scenario: Запись решения
- **КОГДА** пользователь передаёт run_id, approval_id, actor, role, action,
  subject hash и optional comment
- **ТОГДА** CLI MUST записать audited decision после строгой проверки

#### Scenario: Resume waiting run
- **КОГДА** waiting run возобновляется
- **ТОГДА** CLI MUST требовать resolved persisted approval
- **И** MUST применить только target выбранного action
