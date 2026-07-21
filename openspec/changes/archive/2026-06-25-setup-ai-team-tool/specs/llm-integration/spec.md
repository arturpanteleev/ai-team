## ДОБАВЛЕННЫЕ Требования

### Requirement: Заглушка LLM Runtime
Система MUST предоставить заглушку `LLMRuntime` для использования в будущем.

#### Scenario: Заглушка возвращает ошибку
- **КОГДА** вызывается LLMRuntime.Execute
- **ТОГДА** она MUST вернуть `ErrNotImplemented` с сообщением "LLM runtime: пока не реализовано"

### Requirement: Выбор runtime
Система MUST выбирать runtime на основе поля `runtime` в `def.yaml` агента.

#### Scenario: Поле runtime
- **КОГДА** у агента указано `runtime: llm`
- **ТОГДА** система MUST использовать LLMRuntime
- **КОГДА** у агента указано `runtime: agentcli`
- **ТОГДА** система MUST использовать AgentCLIRuntime
