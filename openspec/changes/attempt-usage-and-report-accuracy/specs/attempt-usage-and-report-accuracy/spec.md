# attempt-usage-and-report-accuracy delta

## ADDED Requirements

### Requirement: Per-attempt usage survives pauses

The controller MUST persist attested input and output token counts in each
attempt manifest. Resuming a task MUST reconstruct totals from the durable
attempt manifests exactly once, including superseded attempts. If any invoked
model attempt lacks attested usage, the aggregate MUST be marked unknown and
MUST NOT expose partial sums as the full task total.

#### Scenario: Two pauses preserve task totals

- **WHEN** a task executes attempts, pauses twice, and resumes after each pause
- **THEN** each attempt manifest retains its own token counts
- **AND** the final task totals equal the sum of all attested attempt manifests
- **AND** no attempt is added more than once

#### Scenario: Runtime does not report usage

- **WHEN** an invoked runtime, including OpenCode, provides no attested usage
- **THEN** the CLI displays `нет данных` for input, output, and total tokens
- **AND** no unavailable token count is presented as zero

### Requirement: Task time and execution budget are distinct

The task's total elapsed duration MUST start at its original creation time and
include pauses. `budget.max_execution_time` MUST limit one pipeline execution
invocation; resuming starts a fresh execution budget. Human decision timestamps
MUST be taken from `Decision.DecidedAt` rather than the later resume time.
`budget.max_wall_time` MUST remain accepted as a legacy alias, and configuring
both names MUST be rejected.

#### Scenario: Resume after a human decision

- **WHEN** an operator decides while the task is paused and the worker resumes
  later
- **THEN** the approval event timestamp equals the decision timestamp
- **AND** the final elapsed task duration starts at the original creation time

### Requirement: Reports and usage CLI reflect durable evidence

Final report Checks, Inputs, and Outputs MUST be reconstructed from attempt
manifests after resume. Report start and end timestamps MUST use one timezone.
The usage CLI MUST display only input, output, and total tokens, plus an
optional approximate subscription allocation based on an explicitly configured
monthly subscription amount and recorded monthly token totals. The allocation
MUST be labeled approximate and MUST NOT use API pricing. Missing required
inputs MUST produce `оценка недоступна`.

#### Scenario: Resumed report has attempt data

- **WHEN** a task is resumed after one or more attempts have completed
- **THEN** the final report counts checks and artifacts from those manifests
- **AND** every rendered start/end timestamp is in UTC

#### Scenario: Subscription estimate lacks its inputs

- **WHEN** subscription cost is not configured or a required monthly usage
  total is unknown
- **THEN** the usage CLI displays `оценка недоступна`

#### Scenario: Legacy usage totals may be partial

- **WHEN** the CLI reads a schema v1 usage envelope or uses one in the monthly
  subscription denominator
- **THEN** its input, output, and total tokens are treated as unknown
- **AND** the legacy token sum cannot produce a subscription estimate
