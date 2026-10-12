## ADDED Requirements

### Requirement: Human stage submit API

The Web API MUST provide
`POST /api/runs/{runID}/stages/{stageID}/submit` for typed human result
submissions. The request MUST accept markdown text or UTF-8 base64, a link URL
and link kind, or approval with optional text, plus optional description.
Authenticated submissions MUST derive actor identity and authorized stage role
from the server-side session. The controller MUST enforce the shared result
limits and configured stage contract, resolve only the matching human input
approval, and return the immutable version and SHA-256 receipt. The request
body MUST be bounded to 64 MiB and malformed or conflicting requests MUST NOT
mutate an existing revision.

#### Scenario: Submit a human result

- **WHEN** an authorized actor submits a result for a pending stage input
- **THEN** the response MUST return the resolved approval and its revision ID,
  version, artifact path, and SHA-256

#### Scenario: Invalid result or unauthorized actor

- **WHEN** the result violates the stage contract, exceeds a content limit, or
  the actor lacks the required role
- **THEN** the API MUST reject the submission without resolving the approval

### Requirement: Human stage result history

The Web API MUST return immutable submission history for a stage and result
type at `GET /api/runs/{runID}/artifact-revisions?path=stages/{stageID}/result.{md|link|txt}`.
It MUST accept a new immutable revision through
`POST /api/runs/{runID}/artifact-revisions` only while that stage has a pending
grill question whose pinned artifact revisions include the requested path.
The edit MUST name the latest revision ID and SHA-256 as its base; stale edits
and edits outside a pending grill MUST be rejected. The `grill/new-version`
action then pins the new revision for the next check round.

#### Scenario: Inspect stage result history

- **WHEN** an authorized client requests a stage result history path
- **THEN** the API MUST return the stored revisions in ascending version order

#### Scenario: Edit a pinned human result during a pending grill

- **WHEN** an authorized actor posts a stage result revision against the latest
  revision while that stage has a pending grill question
- **THEN** the API MUST append a new immutable revision and allow
  `grill/new-version` to bind it to the next check round

#### Scenario: Stale or out-of-window stage result edit

- **WHEN** the base revision is stale or no matching grill question is pending
- **THEN** the API MUST reject the edit without adding a revision
