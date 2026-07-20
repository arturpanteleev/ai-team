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
Prompt MUST включать role instructions, feature, task, input file content, directory references, exact output paths и controller-owned service requirements.

#### Scenario: Verdict-bearing agent
- **КОГДА** definition объявляет required verdict
- **ТОГДА** service section MUST содержать единственный канонический marker contract
