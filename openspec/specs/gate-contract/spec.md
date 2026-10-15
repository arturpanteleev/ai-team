# gate-contract Specification

## Purpose

Контракт гейта: определение в общей версионированной библиотеке, ссылка этапа с
точной версией и параметрами, группы, раздельные результаты исполнения и
вердикта. Исполнение групп планировщиком определяется отдельной возможностью.

## Requirements

### Requirement: Gate definitions live in a versioned code library

A gate definition MUST have an ID, an exact positive integer version, a kind
(`builtin`, `llm`, `human`, `external`, or `docker`), a title, a typed parameter
schema, and optionally the result types and link kinds it applies to. The
library MUST live in repository code and MUST change only through a code change;
the Web UI, REST API, and MCP MUST expose the catalog read-only and MUST NOT
accept uploaded or edited definitions. A published ID and version MUST be
immutable: a change of behavior or parameters MUST be a new version, and older
versions MUST remain resolvable. Registering an existing ID and version MUST be
rejected. The catalog MUST be readable at `GET /api/gates/catalog` and include a
digest of each definition.

#### Scenario: Published definition cannot be replaced

- **КОГДА** a definition with an existing ID and version is registered again
- **ТОГДА** registration MUST fail and instruct to publish a new version

#### Scenario: Catalog is read-only

- **КОГДА** a client sends POST, PUT, PATCH, or DELETE to `/api/gates/catalog`
- **ТОГДА** the request MUST be rejected
- **И** GET MUST list every definition with its kind, parameters, and digest

### Requirement: Built-in library wraps the existing checks

The version-1 library MUST contain code gates `required_sections` (parameter
`sections`, `md` results), `verdict_marker` (`md`), `pr_exists`, `pr_open`,
`pr_base_branch` (`link`), `project_checks` and `pr_checks_green` (`link` with
`link_kind: pr`), the LLM gate `agent_review` (parameters `agent`, `mode`,
`max_rounds`), and the human gate `human_approval`. Each version-1 gate MUST keep
the semantics of its schema v5 counterpart.

#### Scenario: Gate applicability

- **КОГДА** `project_checks@1` is referenced from a stage with `link_kind: build`
- **ТОГДА** validation MUST reject the reference naming the required link kind

### Requirement: Stages reference exact versions with declared parameters

A gate reference MUST carry the definition ID and an exact version; a reference
without a version MUST be rejected and "latest" MUST NOT exist. Parameters MUST
be declared by the definition, correctly typed, within bounds, and all required
ones present. A reference MAY carry an instance `name` (unique within the stage,
default the gate ID) and a `mode` of `required` (default) or `advisory`. An
unknown ID, unknown version, undeclared or invalid parameter, inapplicable
result type, empty group, or duplicate instance name MUST be rejected before a
run starts with an error naming the stage and the `exit_gate_groups[i][j]` path.
A reference MAY be written as `id@version` or as a mapping with `gate`,
`version`, `name`, `mode`, and `params`; unknown mapping keys MUST be rejected.

#### Scenario: Unknown version

- **КОГДА** a template references `verdict_marker@9`
- **ТОГДА** validation MUST fail and list the available versions

#### Scenario: Unknown parameter

- **КОГДА** a reference passes a parameter the definition does not declare
- **ТОГДА** validation MUST fail naming the parameter and the accepted ones

#### Scenario: Only the needed gates form several groups

- **КОГДА** a stage lists `[verdict_marker@1]` then `[human_approval@1]`, and
  another stage lists one group of two gates, and a third lists none
- **ТОГДА** each stage MUST use exactly the gates it lists in the listed group
  order

### Requirement: Templates and tasks pin exact definitions

A template MUST bind to exact definitions through its `id@version` references,
and a task MUST use the template YAML it pinned at start. Publishing a newer
version of a definition MUST NOT change any published template or started task:
their references MUST keep resolving to the same definition, and the pin of a
definition MUST include its digest. A newer version MUST apply only when a
template explicitly names it and satisfies its parameter schema.

#### Scenario: New library version after publication

- **КОГДА** the library publishes `required_sections@2` after a template pinned
  `required_sections@1`
- **ТОГДА** the template MUST keep validating and its pins MUST be unchanged
- **И** a template that names `@2` MUST satisfy the version-2 parameters

### Requirement: Attempt result separates execution from verdict

A gate attempt MUST record the task, stage, stage visit, SHA-256 of the checked
result version, gate ID, version and instance name, mode, attempt ID, group
index, executor kind, start and finish times, a redacted reason, and references
to sanitized evidence. Technical `execution` (`pending`, `running`, `succeeded`,
`error`, `timeout`, `canceled`) and content `verdict` (`pass`, `fail`,
`not_applicable`) MUST be separate fields: a verdict MUST be present exactly when
execution is `succeeded`, so an error, a timeout, and a negative verdict never
collapse into one status. `error` and `timeout` MUST be classified as technical
failures that may be retried on the same result version without erasing the
earlier attempt. A required attempt MUST block unless it succeeded with `pass` or
`not_applicable`; an advisory attempt MUST NOT block.

#### Scenario: Verdict on a failed execution

- **КОГДА** an attempt has `execution: timeout` and a `verdict`
- **ТОГДА** the record MUST be invalid

#### Scenario: Blocking semantics

- **КОГДА** a required attempt ends `error`, `timeout`, `canceled`, or
  `succeeded` with `fail`
- **ТОГДА** it MUST block the transition
- **И** an advisory attempt with the same outcome MUST NOT block
