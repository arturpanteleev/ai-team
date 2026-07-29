## MODIFIED Requirements

### Requirement: Subprocess environment isolation

OpenCode subprocess MUST получать только явный allow-list окружения:
фиксированный baseline стандартных OS/locale/session variables и имена,
явно перечисленные в `AI_TEAM_OPENCODE_ENV_ALLOW`, а не полное окружение
вызывающего процесса. В cloud worker infrastructure MUST дополнительно
ограничивать filesystem/process/network границы disposable job.

#### Scenario: Unlisted variable

- **WHEN** process, вызвавший `ai-team run`, содержит variable вне baseline и
  вне `AI_TEAM_OPENCODE_ENV_ALLOW`
- **THEN** OpenCode subprocess MUST NOT получить эту variable

#### Scenario: Explicitly allowed variable

- **WHEN** имя variable перечислено в `AI_TEAM_OPENCODE_ENV_ALLOW`
- **THEN** OpenCode subprocess MUST получить её текущее значение

#### Scenario: Baseline variables always present

- **WHEN** OpenCode subprocess запускается
- **THEN** он MUST получить `PATH` и `HOME` независимо от allow-list

#### Scenario: Запуск в worker

- **КОГДА** agent исполняется через cloud worker
- **ТОГДА** OpenCode MUST видеть exact candidate target и allow-listed
  credentials, но не control-plane process environment целиком
