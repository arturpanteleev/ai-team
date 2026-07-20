## Purpose

Детерминированный OpenCode CLI fixture для end-to-end проверки controller behavior.

## Requirements

### Requirement: OpenCode invocation fixture
Mock MUST принимать `opencode run`, model option и прикреплённый prompt через `--file`.

#### Scenario: Prompt file
- **КОГДА** runtime вызывает `opencode run --file <path> <short-message>`
- **ТОГДА** mock MUST прочитать agent, feature и output contracts из прикреплённого файла

### Requirement: Scoped failure modes
Mock MUST поддерживать normal, rejected, fail и blocked так, чтобы каждый негативный mode влиял только на назначенный этап.

#### Scenario: Rejected review
- **КОГДА** mode=rejected
- **ТОГДА** analyst, architect и coder MUST завершиться нормально
- **И** только reviewer MUST вернуть CHANGES_REQUESTED

#### Scenario: Tester failure
- **КОГДА** mode=fail
- **ТОГДА** только tester MUST вернуть FAIL

#### Scenario: Blocked analyst
- **КОГДА** mode=blocked
- **ТОГДА** analyst MUST создать свежий status marker и MUST NOT создавать обычные outputs

### Requirement: Delivery не мокается LLM
Mock OpenCode MUST NOT выполнять commit, push или PR; delivery MUST тестироваться controller executor fixture с локальным git remote и mock `gh`.

#### Scenario: Successful E2E delivery
- **КОГДА** full E2E достигает delivery
- **ТОГДА** exact run-attributed file MUST быть единственным файлом delivery commit
