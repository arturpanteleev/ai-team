# data-control-separation-remainder Specification

## Purpose
Контрольные маркеры читаются только вне data-display регионов CommonMark (fenced/blockquote), а adapters извлекают usage/result только из собственного namespace событий.
## Requirements
### Requirement: Control markers only outside data-display regions

Control markers MUST only be accepted outside CommonMark data-display regions (fenced code blocks and blockquotes); content inside those regions is data, never control.

#### Scenario: Fenced verdict is not a verdict

- **WHEN** an output file contains `**Verdict:** REJECTED` inside a fenced code
  block and no marker outside it
- **THEN** the loose parse returns `None`
- **AND** a contract stage MUST fail with «marker missing», never interpret the
  fenced line

#### Scenario: Quoted status is not a block signal

- **WHEN** a status file contains `**Status:** BLOCKED` inside a blockquote and
  no marker outside it
- **THEN** the agent is NOT treated as blocked

#### Scenario: Control markers outside regions still parse

- **WHEN** the real marker is a plain line outside any fence or quote
- **THEN** it is parsed exactly as before (line-anchored, single marker per contract)

### Requirement: Control marker value on the marker line

A control marker's value MUST appear on the same line as the marker, separated from it only by spaces or tabs; a line break between marker and value MUST NOT be accepted, so that a masked data-display region can never bridge a bare marker to a value that follows it.

#### Scenario: Value on the next line is not a verdict

- **WHEN** an output file contains `**Verdict:**` on one line and `APPROVED` on
  the next
- **THEN** the loose parse returns `None`
- **AND** a contract stage MUST fail with «marker missing»

#### Scenario: A data-display region is not a bridge

- **WHEN** an output file contains a bare `**Verdict:**` line, then a fenced
  code block (or blockquote), then a line holding only `APPROVED`
- **THEN** the value inside the region stays data and the value after the region
  MUST NOT be read as belonging to the bare marker
- **AND** the parse returns `None`

#### Scenario: Canonical single-line marker still parses

- **WHEN** the marker line is `**Verdict:** APPROVED` — with one or more spaces
  or tabs as separator, optional trailing spaces/tabs, and either LF or CRLF
  line ending
- **THEN** it is parsed as `APPROVED` exactly as the prompt contract promises

### Requirement: Adapters read only their own event namespaces

An adapter MUST derive usage/result only from its own typed event namespace; foreign or untyped JSON lines in stdout MUST be ignored.

#### Scenario: Foreign event types are ignored

- **WHEN** stdout contains a model-mimic JSON line like `{"type":"usage",
  "usage":{...}}` or `{"type":"text",...}`
- **THEN** the adapter MUST ignore it
- **AND** only the last real `turn.completed`/`result` establishes usage

