## ADDED Requirements

### Requirement: Поле gate_after в AgentConfig
Конфиг MUST поддерживать поле `gate_after` для каждого агента.

#### Scenario: gate_after задан
- **КОГДА** конфиг агента содержит `gate_after: true`
- **ТОГДа** pipeline MUST остановиться после этого агента и запросить подтверждение

#### Scenario: gate_after не задан
- **КОГДА** конфиг агента не содержит `gate_after`
- **ТОГДА** pipeline MUST работать без gate после этого агента

### Requirement: Поле gate_before в AgentConfig
Конфиг MUST поддерживать поле `gate_before` для каждого агента.

#### Scenario: gate_before задан
- **КОГДА** конфиг агента содержит `gate_before: true`
- **ТОГДА** pipeline MUST остановиться перед этим агентом и запросить подтверждение

#### Scenario: gate_before не задан
- **КОГДА** конфиг агента не содержит `gate_before`
- **ТОГДА** pipeline MUST работать без gate перед этим агентом

### Requirement: Обратная совместимость формата pipeline
Существующие конфиги с массивом строк MUST продолжать работать без gate-точек.

#### Scenario: Старый формат
- **КОГДА** config.yaml содержит `pipeline: [analyst, architect, coder]`
- **ТОГДА** gate_after и gate_before MUST быть false по умолчанию
- **И** pipeline MUST работать как раньше
