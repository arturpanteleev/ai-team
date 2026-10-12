## ADDED Requirements

### Requirement: Global event feed

The web UI MUST provide a global feed of persisted task events, filterable to
warnings, with live updates and navigation to the task that produced each
event.

#### Scenario: Review all events

- **WHEN** a user opens `/events`
- **THEN** the UI MUST display all event types newest first, in descending
  persisted cursor order, starting from the newest persisted event
- **AND** MUST insert newer events from the live event stream at the top
- **AND** MUST allow the user to load older events before the page's persisted
  cursor

#### Scenario: Live event arrives after a full page

- **GIVEN** more than one page of persisted events exists
- **WHEN** the newest page is shown and a live event with a newer cursor arrives
- **THEN** the UI MUST show the live event above the newest persisted event
- **AND** MUST NOT place it after the oldest loaded event

#### Scenario: Show warnings only

- **WHEN** the user selects the warnings filter
- **THEN** the UI MUST request and display warning lifecycle events only
- **AND** MUST preserve the global event cursor when loading older warnings

#### Scenario: Open the event's task

- **WHEN** a user follows a task link from an event
- **THEN** the UI MUST resolve the stable run ID and open that task's detail page

### Requirement: Newest-first event pages

The event API MUST keep ascending `after` pagination as the default and MUST
support `order=desc` with an optional `before` cursor for newest-first pages.

#### Scenario: Page events newest first

- **WHEN** a client requests `GET /api/events?order=desc&limit=N`
- **THEN** the API MUST return up to N events from the newest persisted cursor
  downward, with `next_cursor` set to the last examined cursor
- **AND** a request with `before=<next_cursor>` MUST return only older events
- **AND** `before` without `order=desc`, or combined with `after`, MUST be
  rejected with 400
