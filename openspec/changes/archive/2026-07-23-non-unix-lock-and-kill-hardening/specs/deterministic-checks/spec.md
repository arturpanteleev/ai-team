## ADDED Requirements

### Requirement: Process tree termination on cancellation
When a stage or check process is canceled or times out, the controller MUST attempt to terminate that process's descendants in addition to the process itself, on every supported platform where a mechanism to do so is available.

#### Scenario: Non-Unix descendant process
- **WHEN** a canceled or timed-out process on a non-Unix platform has spawned child processes of its own
- **THEN** the controller MUST attempt a whole-tree termination
- **AND** MUST fall back to terminating only the direct process if tree termination is unavailable, without failing the cancellation itself
