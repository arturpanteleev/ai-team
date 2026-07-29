## MODIFIED Requirements

### Requirement: Audited human decision

Каждое решение MUST фиксировать trusted actor identity, роль, action,
комментарий и timestamp и MUST проходить role/action/quorum validation. В
cloud mode actor identity и доступные роли MUST поступать из
аутентифицированной server-side session, а не из command body.

#### Scenario: Any quorum

- **КОГДА** quorum равен `any` и аутентифицированный actor с допустимой
  required role принимает решение
- **ТОГДА** approval MUST стать resolved

#### Scenario: All quorum

- **КОГДА** quorum равен `all`
- **ТОГДА** approval MUST стать resolved только после одинакового action от
  каждой required role

#### Scenario: Недопустимая или неподтверждённая роль

- **КОГДА** actor role отсутствует в required roles или ролях principal
- **ТОГДА** decision MUST быть отклонён
