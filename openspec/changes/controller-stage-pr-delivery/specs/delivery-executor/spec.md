## ADDED Requirements

### Requirement: Controller-owned PR result before review
When an agent stage declares a PR result, delivery configuration, and hard `pr_exists` check, the controller MUST validate the pinned attempt manifest SHA-256, approved canonical plan hash, runtime identity, and resolved controller approval before creating or recovering the PR. It MUST persist a controller-owned receipt and project the verified PR URL as a stage result before running the unchanged hard PR checks.

#### Scenario: Default implementation PR is checked before review
- **КОГДА** the implementation attempt and its exact delivery plan have passed required checks and the plan approval is valid
- **ТОГДА** the controller MUST create or recover the PR and publish its URL before evaluating `pr_exists`, `pr_open`, and `pr_base_branch`
- **И** MUST preserve those configured checks and allow later code review and QA stages to consume the controller-owned PR result

#### Scenario: Untrusted stage request
- **КОГДА** a worker submits a mismatched manifest digest, plan hash, runtime identity, or approval
- **ТОГДА** the controller MUST reject the request before any git or remote PR side effect
- **И** MUST NOT accept a worker-supplied PR URL or receipt

### Requirement: Recover controller stage PR receipt and projection
Controller stage delivery MUST be idempotent across crashes before and after remote PR creation, receipt persistence, and result projection. A retry MUST reuse the exact approved commit and PR, persist or reuse the matching receipt, and repair the result projection without creating a duplicate commit or PR.

#### Scenario: Remote PR exists but receipt was not persisted
- **КОГДА** remote PR creation succeeds but the controller loses the response before writing its stage receipt
- **ТОГДА** a retry MUST find and verify the existing PR for the approved branch and commit
- **И** MUST persist the same PR URL without creating another PR

#### Scenario: Receipt exists but projection is missing
- **КОГДА** the controller receipt is durable but writing the project or candidate result projection fails
- **ТОГДА** a retry MUST restore both projections from the receipt
- **И** MUST NOT call delivery again or mutate the attempt manifest

#### Scenario: Resume at code review or QA
- **КОГДА** the implementation attempt is replayed and the run resumes directly at a later review or QA stage
- **ТОГДА** the controller MUST validate or recover the stage receipt before creating that stage's inputs
- **И** MUST provide the PR URL as an immutable `pull-request` input

### Requirement: Terminal attestation remains separate from stage delivery
Terminal run attestation MUST remain a run-level fact and MUST NOT be appended to or used to rewrite the commit already created for review. The terminal record MUST reference the immutable controller stage receipt and independently bind the terminal attestation and runtime identity.

#### Scenario: Finalize after stage delivery
- **КОГДА** the run reaches terminal finalize after PR review and QA
- **ТОГДА** the terminal record MUST identify the exact stage receipt, attempt manifest digest, attestation digest, and runtime identity
- **И** the attempt manifest and delivered commit MUST remain unchanged

### Requirement: Resume an agent delivery stage without re-running the model
When an agent stage with `delivery` stopped in `awaiting_plan` for its exact delivery plan, a resume MUST continue that stage from the prepared plan without invoking the agent runtime again. The continuation MUST re-run the controller checks, MUST NOT change the candidate workspace, and MUST obtain approval for the same canonical plan SHA-256 before any delivery side effect.

#### Scenario: Approved resume after awaiting_plan
- **КОГДА** the implementation attempt stopped for delivery-plan approval and the run is resumed with `--approve-plan <sha256>` for that plan
- **ТОГДА** the controller MUST NOT run the coder again and MUST authorize the unchanged prepared plan for the continuation attempt
- **И** the controller-created PR URL MUST become the stage result of an attempt recorded with `executor: agent`

#### Scenario: Mismatched approval on resume
- **КОГДА** the resume supplies a SHA-256 that differs from the pending plan
- **ТОГДА** the run MUST stop without a commit, push, PR or new model run
