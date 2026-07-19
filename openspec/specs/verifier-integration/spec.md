## Purpose

Интеграция verifier в пайплайн — def.yaml, prompt.md, позиция в registry.

## Requirements

### Requirement: Def.yaml для verifier
Агент verifier ДОЛЖЕН иметь `def.yaml` с определением inputs/outputs.

#### Scenario: Структура def.yaml
- **КОГДА** создаётся `agents/verifier/def.yaml`
- **ТОГДА** def.yaml ДОЛЖЕН содержать:
  - `name: verifier`
  - `description: Unified verification pass`
  - `runtime: agentcli`
  - `inputs`: proposal, specs, review, test-report
  - `outputs`: verification

### Requirement: Prompt для verifier
Агент verifier ДОЛЖЕН иметь `prompt.md` с инструкциями verification pass.

#### Scenario: Структура промпта
- **КОГДА** создаётся `agents/verifier/prompt.md`
- **ТОГДА** промпт ДОЛЖЕН содержать:
  - роль: "Ты — Verifier. Твоя задача — unified verification pass."
  - инструкции по проверке AC
  - инструкции по self-review
  - инструкции по DoD checklist
  - требование на русский язык для verification.md

### Requirement: Интеграция в registry
Verifier ДОЛЖЕН быть добавлен в `Registry.DefaultPipeline()`.

#### Scenario: Default pipeline
- **КОГДА** registry загружает default pipeline
- **ТОГДА** pipeline ДОЛЖЕН содержать: analyst → architect → coder → reviewer → tester → verifier → deployer