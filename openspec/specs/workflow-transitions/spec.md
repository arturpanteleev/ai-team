## Purpose

Спецификация определяет переходы workflow через единый checkpoint и state-machine контракт.

## Requirements

### Requirement: Переход определяется доменным состоянием
Pipeline MUST вычислять execution, decision и outcome до применения checkpoint или loopback.

#### Scenario: Негативный вердикт
- **КОГДА** попытка имеет execution=succeeded и негативный verdict
- **ТОГДА** её outcome MUST быть rejected
- **И** pipeline MUST применить loopback либо `on_negative_verdict`

### Requirement: Варианты интерактивного checkpoint
Интерактивный checkpoint MUST поддерживать `Y`, `n`, `diff` и `summary` без потери текущего состояния.

#### Scenario: diff
- **КОГДА** пользователь вводит `diff`
- **ТОГДА** система MUST показать git diff и повторить тот же checkpoint

#### Scenario: summary
- **КОГДА** пользователь вводит `summary`
- **ТОГДА** система MUST показать свежий stage summary и повторить тот же checkpoint

### Requirement: Legacy normalization
Schema version 3 MUST отклонять legacy checkpoint fields и использовать только
checkpoint policy. Schema versions 1 и 2 MAY принимать `transition`,
`gate_before` и `gate_after`; version 2 мигрирует typed check configuration.

#### Scenario: Одновременные legacy и v2 fields
- **КОГДА** один этап задаёт legacy gate и checkpoint
- **ТОГДА** config validation MUST завершиться ошибкой
