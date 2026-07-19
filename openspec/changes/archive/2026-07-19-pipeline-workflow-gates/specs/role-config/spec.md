## ADDED Requirements

### Requirement: Поле gate_after в AgentConfig
Конфиг ДОЛЖЕН поддерживать поле `gate_after` для каждого агента.

#### Scenario: gate_after задан
- **КОГДА** конфиг агента содержит `gate_after: true`
- **ТОГДа** pipeline ДОЛЖЕН остановиться после этого агента и запросить подтверждение

#### Scenario: gate_after не задан
- **КОГДА** конфиг агента не содержит `gate_after`
- **ТОГДА** pipeline ДОЛЖЕН работать без gate после этого агента

### Requirement: Поле gate_before в AgentConfig
Конфиг ДОЛЖЕН поддерживать поле `gate_before` для каждого агента.

#### Scenario: gate_before задан
- **КОГДА** конфиг агента содержит `gate_before: true`
- **ТОГДА** pipeline ДОЛЖЕН остановиться перед этим агентом и запросить подтверждение

#### Scenario: gate_before не задан
- **КОГДА** конфиг агента не содержит `gate_before`
- **ТОГДА** pipeline ДОЛЖЕН работать без gate перед этим агентом

### Requirement: Обратная совместимость формата pipeline
Существующие конфиги с массивом строк ДОЛЖНЫ продолжать работать без gate-точек.

#### Scenario: Старый формат
- **КОГДА** config.yaml содержит `pipeline: [analyst, architect, coder]`
- **ТОГДА** gate_after и gate_before ДОЛЖНЫ быть false по умолчанию
- **И** pipeline ДОЛЖЕН работать как раньше
