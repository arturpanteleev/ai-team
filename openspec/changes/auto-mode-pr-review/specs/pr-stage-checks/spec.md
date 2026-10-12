# PR stage checks and reviewer evidence

## ADDED Requirements

### Requirement: PR-bound project and GitHub checks

Hard rules `project_checks` and `pr_checks_green` MUST be available on PR link
stages and MUST run identically for results submitted by humans or agents.
`project_checks` MUST run `ai-team gate` against a detached checkout of the PR
head and its exact base; gate policy MUST come from the pinned base. The
controller checkout MUST remain on its original branch and HEAD. `pr_checks_green`
MUST use `gh pr checks` and fail closed if the PR identity changes during the
request. Both rules MUST be bound to one URL/base/head identity, and that exact
identity MUST be retained for reviewer diff preparation. PR URLs MUST use a
trusted GitHub host (github.com by default; enterprise hosts require explicit
configuration).

Commands selected by the pinned base gate policy MUST run in an OS-enforced
untrusted-code sandbox with a read-only host root, writable PR checkout and
private temporary/home directories, network disabled, and credential-bearing
environment variables removed. If the platform cannot establish that sandbox,
`project_checks` MUST block without running a configured check command.

#### Scenario: Check an agent or human PR result

- **GIVEN** a PR stage with either a human-submitted or agent-produced PR URL
- **WHEN** project and GitHub checks run
- **THEN** both rules MUST evaluate the same PR reference without depending on
  the stage executor
- **AND** a failed check MUST block the transition
- **AND** both rules MUST be evaluated against the same URL, base SHA and head SHA

#### Scenario: Candidate attempts to weaken project checks

- **GIVEN** the PR modifies or adds `gate.yaml`
- **WHEN** `project_checks` runs
- **THEN** it MUST use gate policy from the pinned PR base
- **AND** it MUST run the checks against the exact PR-head checkout
- **AND** it MUST not run configured check commands when enforced containment is unavailable

#### Scenario: Reject an untrusted PR host

- **GIVEN** a PR result links to a loopback address or a host outside the trusted GitHub host allowlist
- **WHEN** the controller resolves the PR
- **THEN** it MUST reject the reference before invoking `gh`

### Requirement: Reviewer receives stable PR evidence

When a successful preceding stage provides a PR link, a reviewer stage MUST
receive the `gh pr diff` and PR identity containing URL, base SHA and head SHA,
along with its declared specification and technical-decision inputs. The
controller MUST reject a diff response if the PR identity changes while it is
read. These files MUST be immutable inputs scoped to the run.

#### Scenario: Review an existing PR

- **GIVEN** a preceding successful PR stage and a configured reviewer
- **WHEN** the reviewer starts
- **THEN** the reviewer MUST receive the diff and pinned identity for the same
  PR, plus the configured specifications and technical decision
- **AND** the controller MUST reject the review if URL, base SHA or head SHA
  differs from the identity accepted by the preceding project checks

#### Scenario: Review flow without a PR link

- **GIVEN** a reviewer stage has no preceding successful PR result
- **WHEN** the stage starts
- **THEN** the existing non-PR review flow MUST remain available
