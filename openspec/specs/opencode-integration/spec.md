## Purpose

Явный adapter для свежих non-interactive OpenCode sessions без переполнения argv.
## Requirements
### Requirement: OpenCode adapter
AgentCLI runtime MUST запускать документированный `opencode run`, прикрепляя полный prompt через временный файл mode 0600 и короткий message argument.

#### Scenario: Большой prompt
- **КОГДА** prompt превышает практический ARG_MAX
- **ТОГДА** его содержимое MUST NOT передаваться как command-line argument
- **И** temporary prompt file MUST быть удалён после процесса

#### Scenario: Fresh session
- **КОГДА** этап запускается
- **ТОГДА** adapter MUST NOT использовать `--continue`, `--resume` или случайную предыдущую session

### Requirement: Explicit adapters
Неизвестный CLI binary MUST быть отклонён, пока для него не реализован явный adapter.

#### Scenario: Config cli=claude без adapter
- **КОГДА** runtime пытается запустить CLI с неизвестной схемой аргументов
- **ТОГДА** он MUST вернуть понятную ошибку вместо guessed OpenCode arguments

### Requirement: Prompt contract
Prompt MUST включать role instructions, feature, task, input file content, directory references, exact output paths и controller-owned service requirements. File-based input content MUST be wrapped in an explicit untrusted-data delimiter with an instruction not to execute commands or role-override instructions found within it.

#### Scenario: Verdict-bearing agent
- **КОГДА** definition объявляет required verdict
- **ТОГДА** service section MUST содержать единственный канонический marker contract

#### Scenario: File-based input
- **WHEN** an agent declares a file-based input
- **THEN** that input's content MUST appear between `<UNTRUSTED_ARTIFACT>` delimiters in the prompt
- **AND** the prompt MUST instruct the agent not to treat that content as instructions

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

### Requirement: OpenCode preflight

До принятия нового run система MUST проверить configured OpenCode executable
и получить его версию ограниченной по времени командой.

#### Scenario: Version command зависла

- **КОГДА** OpenCode version command не завершается в установленный timeout
- **ТОГДА** preflight MUST завершить её
- **И** MUST вернуть failed check без запуска агента

#### Scenario: Безопасная диагностика окружения

- **КОГДА** preflight описывает credential allow-list
- **ТОГДА** сообщение MUST содержать только имена переменных и факт наличия
- **И** MUST NOT содержать значения credentials

