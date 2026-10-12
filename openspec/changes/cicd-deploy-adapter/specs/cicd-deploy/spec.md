## ADDED Requirements

### Requirement: Controller-owned deploy stage

The controller MUST execute a template stage with `result: link`,
`link_kind: build` and an agent of `kind: deploy` without a model runtime. The
deploy MUST use the commit SHA from the controller receipt of an earlier PR
delivery stage. Before triggering any build the controller MUST prove with local
Git, after fetching the configured branch from `origin`, that this exact SHA is
an existing commit reachable from that branch. Provider or GitHub responses MUST
NOT be trusted for this decision. Existing templates without such a stage MUST
keep their behavior.

#### Scenario: The PR commit is merged

- **WHEN** the deploy stage runs and the receipt commit is an ancestor of the
  fetched deploy branch
- **THEN** the controller MUST trigger exactly one build pinned to that SHA

#### Scenario: The commit is not merged or no PR commit exists

- **WHEN** the receipt commit is not reachable from the deploy branch, resolves
  to another object, or no controller PR receipt exists
- **THEN** the controller MUST NOT trigger a build
- **AND** the stage attempt MUST fail

#### Scenario: The process has no provider

- **WHEN** a deploy stage runs in a process that was not given a CI/CD provider,
  such as a sandboxed worker
- **THEN** the stage MUST fail without contacting the CI/CD system

### Requirement: Build wait and result

The controller MUST poll the build status until it finishes, the configured
timeout elapses or the task is canceled. On timeout or cancellation it MUST ask
the provider to cancel the build. The stage MUST succeed only when the build
finished successfully and the provider reports exactly the expected revision;
the build URL MUST then be written as the stage's link output. A retried stage
for the same run, stage and SHA MUST resume or reuse the recorded build instead
of triggering another one. Every failure MUST be recorded as an
`integration_error` task event with `integration: cicd`.

#### Scenario: Build succeeds

- **WHEN** the build finishes with success for the expected revision
- **THEN** the stage link output MUST contain the build URL

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
or task events.

#### Scenario: TeamCity redirects to a foreign host

- **WHEN** a TeamCity request is answered with a redirect to another host
- **THEN** the foreign host MUST NOT receive the request
- **AND** the error MUST NOT contain the token or either URL

#### Scenario: TeamCity rejects the token

- **WHEN** TeamCity answers with an error status
- **THEN** the error MUST contain only the method, API path and status code
