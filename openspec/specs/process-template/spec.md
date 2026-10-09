# process-template Specification

## Purpose

Schema v5 stores the project's single process template as ordered stages and
explicit backward returns, then compiles that declaration into the workflow
graph contract.

## Requirements

### Requirement: One schema v5 template per project

`config.yaml` MUST contain one `template` ID, a human-readable `title`, and an
ordered non-empty `stages` list. Stage IDs MUST be unique. A stage MUST declare
`id`, `title`, `function`, and `result`; result MUST be `md`, `link`, or
`approve`. `executor` defaults to `human` and MUST be `human` or `agent`; an
agent-executed stage MUST reference an existing `agent`.

#### Scenario: Valid stage references

- **КОГДА** schema v5 содержит stage agent references
- **ТОГДА** config MUST reject references absent from the agent registry
- **И** stage IDs MUST remain independent of agent names

#### Scenario: Invalid stage declaration

- **КОГДА** stage IDs repeat, result or executor is invalid, or an
  agent-executed stage has no agent
- **ТОГДА** config MUST be rejected before a run starts

### Requirement: Stage result constraints

`link_kind` MUST be `pr`, `build`, or `other` and is valid only for
`result: link`. `required_sections` MAY be set only for `result: md`. `confirm` MUST be
`required` or `auto`; its default MUST be `auto` for `approve` and `required`
for other results. Optional stage fields MUST be strictly decoded.

#### Scenario: Invalid result metadata

- **КОГДА** a stage has an invalid link kind or required sections on a
  non-markdown result
- **ТОГДА** config MUST be rejected

### Requirement: Backward returns and visit limits

Each `returns` entry MUST reference existing stages and point from a later
stage to an earlier stage. A positive `max_visits` MAY be set per return target
or route; omitted limits MUST default to three visits for every return target.

#### Scenario: Forward return rejected

- **КОГДА** a return targets the same or a later stage
- **ТОГДА** config MUST be rejected before graph compilation

### Requirement: Generate graph from template order

The compiled graph MUST contain one node per stage in the same order. Each
stage MUST have a `passed` edge to the following stage, with the final stage
leading to `$complete`. Each declared return MUST compile into a rejected
transition to its exact backward target, and its target MUST carry the
effective `max_visits` limit.

#### Scenario: Ordered forward transitions

- **КОГДА** a valid template is compiled
- **ТОГДА** every non-terminal stage's `passed` edge MUST target the next
  stage ID in declaration order
- **И** the terminal stage's `passed` edge MUST target `$complete`

#### Scenario: Return route compilation

- **КОГДА** a valid backward return is declared
- **ТОГДА** the compiled graph MUST expose its exact target as a return action
- **И** the target node MUST have the declared or default visit limit

### Requirement: Profile presets materialize templates

`ai-team init` MUST write the built-in `idea-to-prod` template by default.
`fast`, `standard`, and `regulated` presets MUST be serialized as ordinary
schema v5 templates with one template ID each; they MUST NOT require a separate
runtime profile representation.

#### Scenario: Default init template

- **КОГДА** `ai-team init` runs without a profile override
- **ТОГДА** it MUST write `schema_version: 5` and `template: idea-to-prod`

#### Scenario: Profile init

- **КОГДА** init runs with `--profile fast`, `standard`, or `regulated`
- **ТОГДА** the selected template's stages, returns, and limits MUST be
  serialized in `.ai-team/config.yaml`
