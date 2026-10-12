## ADDED Requirements

### Requirement: Controller-owned deploy stage

The controller MUST execute a template stage with `result: link`,
`link_kind: build` and an agent of `kind: deploy` without a model runtime. The
deploy MUST start from the pull request URL and head commit in the controller
receipt of an earlier PR delivery stage. The controller MUST wait, bounded by
`merge_timeout`, until GitHub reports the pull request as merged into the
configured branch. GitHub only names the merge commit; local Git, after fetching
the branch from `origin`, MUST prove that the merge commit is reachable from the
branch tip and contains the pull request change: either the PR head is its
ancestor, or its own diff equals the PR diff against their merge base ignoring
line numbers. The deployed revision MUST be the fetched branch tip, never the
PR head, so that changes merged after the PR are not rolled back. Existing
templates without such a stage MUST keep their behavior.

#### Scenario: Squash merge after the branch moved on

- **WHEN** the PR forked from commit X, the branch later received Y, the PR was
  squash-merged as S and the branch then received Z
- **THEN** the controller MUST trigger exactly one build pinned to the branch
  tip containing Y, S and Z

#### Scenario: The pull request is not merged or does not match

- **WHEN** the PR stays open past `merge_timeout`, is closed without merge, is
  merged into another branch, the merge commit is not on the branch, or the
  squash commit's change differs from the PR change
- **THEN** the controller MUST NOT trigger a build
- **AND** the stage attempt MUST fail

#### Scenario: No controller PR receipt or no provider

- **WHEN** no controller PR receipt exists, or the process was not given a
  CI/CD provider, such as a sandboxed worker
- **THEN** the stage MUST fail without contacting the CI/CD system

### Requirement: Build wait and result

The controller MUST poll the build status until it finishes, the configured
timeout elapses or the task is canceled. On timeout or cancellation it MUST ask
the provider to cancel the build. The stage MUST succeed only when the build
finished successfully in the configured build configuration and the provider
reports exactly the deployed revision; the build URL MUST then be written as
the stage's link output. Transient poll failures MUST be retried until the
deadline. A retried stage for the same run, stage and revision MUST resume or
reuse the recorded build instead of triggering another one, and MUST re-read
that build from the provider instead of trusting the stored receipt; a receipt
of another run or stage MUST be rejected. Every failure MUST be recorded as an
`integration_error` task event with `integration: cicd`.

#### Scenario: Build succeeds

- **WHEN** the build finishes with success for the expected revision
- **THEN** the stage link output MUST contain the build URL

#### Scenario: A receipt was tampered with

- **WHEN** the stored receipt claims a successful build with a foreign link or
  points to a build of another build configuration
- **THEN** the controller MUST use the status and link read from the provider
- **AND** MUST reject a build of another build configuration

#### Scenario: Build fails, times out or builds another revision

- **WHEN** the build fails, the wait exceeds its timeout, or the reported
  revision differs from the expected SHA
- **THEN** the stage MUST fail, no build link MUST be published
- **AND** a timed-out build MUST be canceled in the CI/CD system

### Requirement: TeamCity credentials and egress

The TeamCity token MUST be read only from the controller environment variable
named by `cicd.teamcity.token_env`. `base_url` MUST be an https URL without
credentials, query or fragment. Redirects to another host or scheme MUST NOT be
followed. The build URL used as the stage result MUST be https on the configured
host. The token and request URLs MUST NOT appear in errors, evidence, receipts
or task events, and child `git`/`gh` processes MUST NOT inherit the token
variable.

#### Scenario: TeamCity redirects to a foreign host

- **WHEN** a TeamCity request is answered with a redirect to another host
- **THEN** the foreign host MUST NOT receive the request
- **AND** the error MUST NOT contain the token or either URL

#### Scenario: TeamCity rejects the token

- **WHEN** TeamCity answers with an error status
- **THEN** the error MUST contain only the method, API path and status code
