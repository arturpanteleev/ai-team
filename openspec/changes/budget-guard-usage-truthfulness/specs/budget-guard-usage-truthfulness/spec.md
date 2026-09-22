# budget-guard-usage-truthfulness delta (P1-7)

## ADDED Requirements

### Requirement: Hard run budget limits (wall-time and attempts)

Every run MUST be constrained by a hard budget: maximum wall-time and maximum
total number of stage attempts. These limits MUST apply even when no
`budget` section is configured (canonical defaults), so a run can never run
unbounded.

#### Scenario: Budget section is honored

- **WHEN** `budget.max_wall_time` and `budget.max_attempts` are set in config
- **THEN** the run is cancelled after the wall-time expires
- **AND** no attempt beyond `max_attempts` is started

#### Scenario: Defaults apply without budget section

- **WHEN** no `budget` section is configured
- **THEN** the run is still bounded by canonical defaults
  (wall-time 24h, attempts 100)

#### Scenario: Invalid budget is rejected at load

- **WHEN** `max_wall_time` does not parse as a positive Go duration or
  `max_attempts` is negative
- **THEN** config validation MUST fail

#### Scenario: Budget breach is reported as the run reason

- **WHEN** the budget is exceeded
- **THEN** the run stops with a reason naming the exceeded limit
  (`max_wall_time` or `max_attempts`)

### Requirement: Usage tokens/cost accepted only from attested adapter source

Token and cost figures MUST NOT be faked by the harness. They are accepted
ONLY from an adapter that supports the `usage-reported` capability and parse
results from the agent's own output. The usage envelope tracks whether the
values are attested.

#### Scenario: Attested usage is persisted

- **WHEN** an adapter with `usage-reported` capability provides parseable usage
- **THEN** the aggregated usage is written to the run usage envelope
  with `usage_reported: true`
- **AND** `tokens_input`, `tokens_output` and `cost_usd` hold the sums

#### Scenario: Unattested usage is not reported

- **WHEN** the adapter cannot be parsed or does not attest usage
- **THEN** the envelope keeps `tokens_unknown: true`
- **AND** no token or cost values are claimed