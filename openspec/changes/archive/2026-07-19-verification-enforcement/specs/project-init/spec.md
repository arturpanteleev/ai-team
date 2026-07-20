## MODIFIED Requirements

### Requirement: Конфиг по умолчанию
`ai-team init` MUST сериализовать полный дефолтный конфиг (yaml.Marshal структуры Default), а не собирать YAML вручную.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать: `pipeline` со структурными элементами (включая `gate_after: true` у analyst и architect, `gate_before: true` у deployer, `max_retries` у coder), `cli: opencode`, `effort: medium`, `stage_timeout: 30m`
- **И** повторный `init` MUST NOT перезаписывать существующий конфиг

### Requirement: Gitignore
Система MUST гарантировать, что `.ai-team/` исключён из git.

#### Scenario: .gitignore существует
- **КОГДА** `.gitignore` существует и не содержит `.ai-team`
- **ТОГДА** система MUST дописать `.ai-team/`

#### Scenario: .gitignore отсутствует
- **КОГДА** `.gitignore` не существует, а targetDir — git-репозиторий
- **ТОГДА** система MUST создать `.gitignore` с `.ai-team/`

### Requirement: Структура директорий
`init` MUST создавать только используемые директории.

#### Scenario: Актуальная раскладка
- **КОГДА** `ai-team init` запускается
- **ТОГДА** MUST быть созданы `.ai-team/artifacts/tasks/`, `.ai-team/reports/`, `.ai-team/logs/`
- **И** MUST NOT создаваться неиспользуемые `artifacts/product/`, `artifacts/tech/`, `artifacts/reviews/`
