## ADDED Requirements

### Requirement: Local verify matches CI
The `make verify` command MUST exercise gofmt formatting, the coverage gate,
and end-to-end tests, in addition to its existing checks, so that a
contributor running it locally exercises the same checks as the `lint`,
`unit-tests`, `race-tests` and `e2e-tests` CI jobs.

#### Scenario: Unformatted file
- **WHEN** a tracked `.go` file is not gofmt-formatted
- **THEN** `make verify` MUST fail with the unformatted file names listed

#### Scenario: Coverage below threshold
- **WHEN** aggregate test coverage is below the CI-enforced threshold
- **THEN** `make verify` MUST fail

#### Scenario: E2E test failure
- **WHEN** an end-to-end test fails
- **THEN** `make verify` MUST fail
