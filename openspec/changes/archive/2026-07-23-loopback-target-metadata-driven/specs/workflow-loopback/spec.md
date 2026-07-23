## ADDED Requirements

### Requirement: Default loopback target is metadata-driven
When a stage does not declare `loopback_to` explicitly, the system MUST
select the closest preceding stage whose definition declares
`mutation: source` as the default loopback target, rather than matching a
fixed name.

#### Scenario: Renamed source-writing stage
- **WHEN** a pipeline's source-writing stage is not named "coder" and no stage declares `loopback_to` explicitly
- **THEN** a negative verdict MUST still trigger loopback to that renamed stage, provided its definition declares `mutation: source`

#### Scenario: No eligible stage
- **WHEN** no preceding stage declares `mutation: source`
- **THEN** loopback MUST NOT trigger, matching the existing behavior for an unmatched explicit `loopback_to` target
