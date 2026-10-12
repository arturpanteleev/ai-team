## ADDED Requirements

### Requirement: Browser stage result submission

The task page MUST let the user submit the current stage's configured result
type through the existing stage results endpoint when the task detail response
lists `results` in `allowed_actions`. Markdown results MUST offer a
rendered preview and loading the text from a UTF-8 file. Link results MUST use
the link kind pinned in the stage template and MUST reject non-HTTP(S) URLs
before submission. Approve results MUST accept optional text sent as the
submission note. Client-side limits MUST NOT be looser than the server's.

#### Scenario: Submit each result type

- **WHEN** the current stage expects `md`, `link`, or `approve`
- **THEN** the page MUST show the matching form and send the matching
  submission with the stage's pinned `link_kind` for links

#### Scenario: Reject an invalid link or oversized Markdown

- **WHEN** the URL is not HTTP(S), contains credentials, or the Markdown is
  larger than the server's byte limit
- **THEN** the page MUST explain the problem and MUST NOT send the submission

#### Scenario: Empty description

- **WHEN** “Что сделано” is empty
- **THEN** the page MUST show a warning
- **AND** MUST still allow the submission, leaving `description_missing` to
  the server

### Requirement: Stage result draft survives reload

The task page MUST keep the unsent result form as a browser-local draft keyed
by run ID and stage ID. Storage failures MUST NOT break the page.

#### Scenario: Reload with a draft

- **WHEN** the user reloads the page while a draft exists for the current stage
  and results are allowed
- **THEN** the page MUST reopen the result form with the draft's values

#### Scenario: Server rejects or accepts the submission

- **WHEN** the server rejects the submission
- **THEN** the draft MUST remain
- **WHEN** the server accepts the submission
- **THEN** the draft MUST be removed

#### Scenario: Storage unavailable

- **WHEN** browser storage is unavailable or full
- **THEN** the form MUST keep working in memory and MUST tell the user the
  draft is not saved
