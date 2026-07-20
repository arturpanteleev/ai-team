## ADDED Requirements

### Requirement: Поле transition в AgentConfig
Конфиг MUST поддерживать поле `transition` для каждого агента.

#### Scenario: transition задан
- **КОГДА** конфиг агента содержит `transition: by_confirm`
- **ТОГДА** система MUST использовать это значение при выборе поведения

### Requirement: Поле max_retries в AgentConfig
Конфиг MUST поддерживать поле `max_retries` для каждого агента.

#### Scenario: max_retries задан
- **КОГДА** конфиг агента содержит `max_retries: 2`
- **ТОГДА** система MUST разрешить до 2 ретраев для этого агента
