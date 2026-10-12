## ADDED Requirements

### Requirement: Global event feed

The web UI MUST provide a global feed of persisted task events, filterable to
warnings, with live updates and navigation to the task that produced each
event.

#### Scenario: Review all events

- **WHEN** a user opens `/events`
- **THEN** the UI MUST display all event types in persisted cursor order
- **AND** MUST append newer events from the live event stream
- **AND** MUST allow the user to load the next page after its persisted cursor

#### Scenario: Show warnings only

- **WHEN** the user selects the warnings filter
- **THEN** the UI MUST request and display warning lifecycle events only
- **AND** MUST preserve the global event cursor when loading more warnings

#### Scenario: Open the event's task

- **WHEN** a user follows a task link from an event
- **THEN** the UI MUST resolve the stable run ID and open that task's detail page
