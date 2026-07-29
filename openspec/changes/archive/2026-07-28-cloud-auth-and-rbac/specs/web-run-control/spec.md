## MODIFIED Requirements

### Requirement: Browser approval command

Browser decision MUST использовать общий approval store и ту же subject,
action и quorum validation, что CLI decision. В cloud mode actor identity и
доступные роли MUST определяться аутентифицированной browser-session.

#### Scenario: Допустимое решение

- **КОГДА** authenticated actor с required role отправляет допустимый action
  и exact subject
- **ТОГДА** decision MUST быть сохранён с trusted actor/comment/timestamp
- **И** run MUST стать доступен для resume

#### Scenario: Stale решение

- **КОГДА** subject не совпадает с pending approval
- **ТОГДА** API MUST отклонить решение без изменения run

#### Scenario: Подмена identity

- **КОГДА** command body содержит actor ID, отличный от session principal
- **ТОГДА** это значение MUST NOT попасть в audit decision
