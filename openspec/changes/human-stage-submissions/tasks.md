## Implementation

- [x] Add immutable stage submission revisions with exact bytes, metadata,
  versioning, SHA-256, idempotent retries, and concurrency conflicts.
- [x] Bind typed submission metadata to the input approval decision and preserve
  markdown boundary whitespace across storage and replay.
- [x] Add shared typed-result validation and controller-owned
  `description_missing` event validation/replay.
- [x] Add the stage submission REST route and stage revision history access.
- [x] Add `ai-team stage submit` for file/text, link, and approve results.
- [x] Add regression coverage for approval/store concurrency, worker event
  boundaries, pipeline attempt evidence, Web API, and CLI.
- [x] Run `openspec validate --all --strict --no-interactive`.
- [x] Run `make verify`.
