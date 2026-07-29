## MODIFIED Requirements

### Requirement: Init
`ai-team init` MUST создать `.ai-team/config.yaml`, artifact tasks, reports и
logs directories. В Git repository команда MUST обеспечить ignore
`.ai-team/`, не изменяя workspace по умолчанию.

#### Scenario: Флаги init
- **КОГДА** пользователь передаёт `--target <path>` и/или
  `--write-gitignore` в любом поддерживаемом порядке
- **ТОГДА** CLI MUST применить оба флага
- **И** неизвестный флаг или отсутствующее значение MUST привести к
  ненулевому exit code

#### Scenario: Поведение по умолчанию
- **КОГДА** `init` запускается в Git repository без `--write-gitignore`
- **ТОГДА** команда MUST использовать локальный Git exclude
- **И** MUST NOT изменять `.gitignore`
