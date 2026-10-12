# Auto run mode

## ADDED Requirements

### Requirement: Run-scoped automatic stage ownership

An auto-mode run MUST pin a run-specific template projection in which stages
with an agent execute as agents and ordinary graph confirmations proceed
automatically. It MUST NOT modify the active project template. Stages without
an agent, question answers, agreed-spec approval and delivery-plan approval
MUST remain human-owned.

#### Scenario: Start and resume an auto run

- **GIVEN** a valid stage template with agent and agentless stages
- **WHEN** a run starts in `auto` mode and is later resumed
- **THEN** the pinned run template MUST retain automatic execution for agent
  stages and human ownership for agentless stages
- **AND** the active project template MUST remain unchanged

#### Scenario: Failed stage check receives one retry

- **GIVEN** an agent stage's hard or checker-agent result fails
- **WHEN** the controller records the failed report
- **THEN** the agent MUST receive exactly one retry with immutable feedback bound
  to that report
- **AND** if that retry also fails, the run MUST pause for a human
  `check_decision` before any later stage begins

#### Scenario: Retry recovery

- **GIVEN** the retry event is durable but the retry attempt has not started
- **WHEN** the run resumes
- **THEN** the controller MUST recreate the feedback from the same failed report
- **AND** MUST NOT create a second retry for that failed attempt

#### Scenario: Human-only checkpoints remain human

- **GIVEN** auto mode reaches an agentless stage, a question, agreed-spec
  approval, or delivery plan
- **WHEN** the run reaches that checkpoint
- **THEN** it MUST wait for the required human input or approval
