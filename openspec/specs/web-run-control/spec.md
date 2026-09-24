# web-run-control Specification

## Purpose
Write API web-сервера: start/resume/cancel/approval decisions поверх общего RunEngine с session+CSRF защитой.
## Requirements
### Requirement: Async run commands
Web control plane MUST запускать start, resume и cancel через общий
`RunEngine`, не блокируя HTTP request на время AI pipeline.

#### Scenario: Новый run
- **КОГДА** авторизованный browser отправляет feature и task
- **ТОГДА** API MUST зарезервировать run_id и вернуть 202
- **И** MUST запустить pipeline асинхронно с этим run_id

#### Scenario: Duplicate worker
- **КОГДА** run уже исполняется в текущем control plane
- **ТОГДА** повторный resume MUST вернуть conflict

#### Scenario: Restart
- **КОГДА** process-local worker был потерян после restart
- **ТОГДА** persisted non-terminal run MUST оставаться доступным для resume

### Requirement: Browser approval command

Browser decision MUST использовать общий approval store и ту же subject,
action и quorum validation, что CLI decision. Actor identity и роль решения
MUST определяться аутентифицированной browser-session и `required_roles`
самого approval, а не телом запроса.

#### Scenario: Допустимое решение

- **КОГДА** authenticated actor с required role отправляет допустимый action
  и exact subject
- **ТОГДА** decision MUST быть сохранён с trusted actor/role/comment/timestamp
- **И** run MUST стать доступен для resume

#### Scenario: Stale решение

- **КОГДА** subject не совпадает с pending approval
- **ТОГДА** API MUST отклонить решение без изменения run

#### Scenario: Роль вне principal

- **КОГДА** ни одна из `required_roles` approval не принадлежит session
  principal
- **ТОГДА** API MUST отклонить решение и MUST NOT записать его в store

#### Scenario: Подмена identity

- **КОГДА** command body объявляет actor ID или роль
- **ТОГДА** API MUST отклонить такой запрос, и эти значения MUST NOT попасть
  в audit decision

### Requirement: Write protection
Каждый web write request MUST пройти authentication, session и CSRF
validation поверх Host/Origin policy. Write API без настроенной identity
MUST быть недоступен (fail-closed).

#### Scenario: Identity не настроена
- **КОГДА** сервер запущен без authenticator
- **ТОГДА** любой write request MUST быть отклонён до исполнения команды

#### Scenario: Нет session
- **КОГДА** write request не содержит выданную server-ом session-cookie
- **ТОГДА** API MUST вернуть 401

#### Scenario: Нет CSRF
- **КОГДА** session валидна, но CSRF header отсутствует или неверен
- **ТОГДА** API MUST вернуть 403
