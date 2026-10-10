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
for other results. `skippable` MAY be set to allow an explicit skip with a
non-empty reason. Optional stage fields MUST be strictly decoded.

#### Scenario: Invalid result metadata

- **КОГДА** a stage has an invalid link kind or required sections on a
  non-markdown result
- **ТОГДА** config MUST be rejected

#### Scenario: Unsupported no-confirm mode

- **КОГДА** a stage declares `confirm: none`
- **ТОГДА** config MUST be rejected because schema v5 supports only `required`
  and `auto`

### Requirement: Human executor stages collect typed results

A stage with `executor: human` MUST stop for a typed `input` approval bound to
the stage ID, result type, output name/path, declared input digests, and current
candidate identity when present. `result: approve` MUST collect an explicit
`approve` or `reject` action; `result: md` and `result: link` MUST collect a
`submit` or `reject` action. `--approve-gates` MUST NOT synthesize a human
result. The request MUST leave the stage without a started attempt; after a
valid submission, the controller MUST resume that same stage and publish the
human result as its attempt output before applying the ordinary graph edge and
its independent confirmation policy.

#### Scenario: Human stage pauses and resumes with markdown

- **КОГДА** run reaches a `human` stage with `result: md`
- **ТОГДА** it MUST persist `kind: input`, set lifecycle to `waiting`, and
  create no unfinished attempt
- **КОГДА** an authorized actor submits markdown for the exact subject
- **ТОГДА** the same stage MUST resume with the submitted bytes as its output,
  record the actor and `executor: human`, and then follow its configured graph
  edge and confirmation gate

#### Scenario: Human link result

- **КОГДА** run reaches a human `result: link` stage
- **ТОГДА** submission MUST be validated against `link_kind` and published as
  the stage's output artifact, so the attempt can be consumed or verified like
  other stage outputs

#### Scenario: Invalid or stale human input

- **КОГДА** input is empty, malformed for its declared result, or bound to a
  changed stage/input/candidate subject
- **ТОГДА** it MUST NOT advance the stage or replace previously submitted
  content

### Requirement: Backward returns and visit limits

Each `returns` entry MUST reference existing stages and point from a later
stage to an earlier stage. A positive `max_visits` MAY be set per return target
or route; omitted limits MUST default to three visits for every return target.
The engine MUST expose only configured return targets, require a non-empty
reason for a return decision, invalidate downstream attempts after the target,
and re-execute every stage between the selected target and the returning stage.
The reason MUST be delivered to the target executor as read-only feedback.

#### Scenario: Forward return rejected

- **КОГДА** a return targets the same or a later stage
- **ТОГДА** config MUST be rejected before graph compilation

#### Scenario: Return repeats the intervening stages

- **КОГДА** QA at a later stage selects a configured return to `implementation`
- **ТОГДА** the engine MUST rerun `implementation`, each intervening stage,
  and QA in order
- **И** the target executor MUST receive the persisted human reason as
  read-only feedback

#### Scenario: Return without a reason

- **КОГДА** a return action has an empty or whitespace-only reason
- **ТОГДА** the decision MUST be rejected without resolving the approval

#### Scenario: Return visit limit is reached

- **КОГДА** the selected return would start a target whose `max_visits` is
  exhausted
- **ТОГДА** the run MUST fail before starting another target attempt

### Requirement: Generate graph from template order

The compiled graph MUST contain one node per stage in the same order. Each
stage MUST have a `passed` edge to the following stage, with the final stage
leading to `$complete`. A `skippable: true` stage MUST additionally have a
`skipped` edge to the following stage (or `$complete` for the final stage).
Each declared return MUST compile into a rejected
transition to its exact backward target, and its target MUST carry the
effective `max_visits` limit. A forward edge MUST require approval when its
source stage has `confirm: required` or declares one or more return routes. A
`confirm: auto` stage without return routes MUST permit the transition without
an edge approval. When return routes exist, the forward decision MUST expose
`approve` and only the configured `return_to_*` targets, so return remains an
available stage action even with `confirm: auto`. Backward return edges MUST
continue to require approval. This rule applies to schema v5 templates; legacy
workflow graphs retain their existing approval validation.

#### Scenario: Ordered forward transitions

- **КОГДА** a valid template is compiled
- **ТОГДА** every non-terminal stage's `passed` edge MUST target the next
  stage ID in declaration order
- **И** the terminal stage's `passed` edge MUST target `$complete`

#### Scenario: Stage confirmation controls forward approval

- **КОГДА** a non-terminal stage has `confirm: auto` and no configured returns
- **ТОГДА** its `passed` edge MUST be valid without an approval policy
- **КОГДА** a non-terminal stage has `confirm: required`
- **ТОГДА** its `passed` edge MUST carry an approval policy
- **КОГДА** a stage has configured return routes, regardless of its `confirm`
  setting
- **ТОГДА** its `passed` edge MUST carry `approve` and those exact
  `return_to_*` actions
- **И** backward return edges MUST carry approval policies regardless of
  `confirm`

#### Scenario: Return route compilation

- **КОГДА** a valid backward return is declared
- **ТОГДА** the compiled graph MUST expose its exact target as a return action
- **И** the target node MUST have the declared or default visit limit

#### Scenario: Skip edge compilation

- **КОГДА** a template stage declares `skippable: true`
- **ТОГДА** its compiled graph MUST contain a `skipped` edge to the next stage
- **КОГДА** a stage is not skippable
- **ТОГДА** its compiled graph MUST NOT contain a `skipped` edge

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

### Requirement: Project template editor publishes validated versions

The web interface MUST display the project's single selected template as an
ordered flow with its stage roles, results, executors, and backward returns.
An authorized Product Owner or Architect MAY edit its YAML. Before publish,
the server MUST validate strict YAML fields and document count, schema and
stage constraints, agent references, graph reachability and visit limits, and
cloud approval roles. A publish MUST atomically replace the active project
template and retain the published YAML under its content-addressed immutable
version. Concurrent edits MUST NOT silently overwrite a newer active version.

#### Scenario: Invalid template cannot be published

- **КОГДА** YAML contains an unknown field, unavailable agent or cloud role,
  unreachable stage, or unbounded return
- **ТОГДА** validation MUST return a diagnostic and publish MUST leave the
  active template unchanged

#### Scenario: Publish with stale active version

- **КОГДА** two editors load one active version and the first publishes a
  replacement
- **И** the second editor publishes using the old version as its expected
  version
- **ТОГДА** the second publish MUST be rejected without changing the active
  template

### Requirement: Tasks retain their selected template version

The system MUST create an immutable template pin when a task first starts.
Every continuation, retry, or recovery run for that task MUST reference the
same task pin even after the project publishes a newer template. A newly
created task MUST pin the currently active project template. Run-scoped pin
records MAY reference a task pin but MUST NOT select a new template version
for an existing task. Before replacing the active template, publish MUST
backfill every nonterminal task that predates pinning from the currently active
template, so those tasks remain resumable after a server restart.

#### Scenario: Continue an existing task after publish

- **КОГДА** a task is created from template version A and the project later
  publishes version B
- **И** the task is continued, retried, or recovered
- **ТОГДА** each run for that task MUST resolve the immutable version A pin

#### Scenario: Publish while older tasks are still running

- **КОГДА** the system has nonterminal tasks without template pins
- **И** a new template version is published
- **ТОГДА** those tasks MUST first pin the previously active version
- **И** they MUST continue to resolve that version after a server restart

#### Scenario: Start a task after publish

- **КОГДА** a new task starts after version B becomes active
- **ТОГДА** it MUST pin and resolve version B
