## ADDED Requirements

### Requirement: Task timeline

The task detail page MUST show a timeline with one lane per stage of the pinned
template. Lane segments MUST be derived only from recorded task events and
MUST distinguish the states waiting for executor, work, check and awaiting
confirmation. Each segment MUST indicate whether a human or an agent executed
it. Returns, check overrides and skips MUST be marked on the lane of the stage
that issued them. State, executor and marks MUST NOT be conveyed by colour
alone.

#### Scenario: Stage not started

- **WHEN** a stage has no recorded events or attempts
- **THEN** its lane MUST NOT show a bar and MUST say that the stage has not started

#### Scenario: Unfinished stage

- **WHEN** the task is still running and a stage interval has not ended
- **THEN** the segment MUST extend to the current time and be marked as ongoing

#### Scenario: Unknown event type

- **WHEN** the task feed contains an event type the timeline does not know
- **THEN** the timeline MUST NOT fail and MUST NOT change any stage state because of it

#### Scenario: Accessible timeline

- **WHEN** a user cannot perceive the bars
- **THEN** the page MUST provide a table of the same segments and marks
- **AND** lanes MUST be reachable and switchable with the keyboard

### Requirement: Stage card

Selecting a lane MUST open a stage card showing the executor and actors, the
interval and time per state, the result, result versions, check rounds,
attempts with log links, tokens and the approximate subscription-share
estimate, the stage's marks, and links to the stage's events in the task feed.

#### Scenario: Unknown usage

- **WHEN** tokens or the subscription estimate for the stage are unknown or the metrics request fails
- **THEN** the card MUST show "нет данных" and MUST NOT show zero
