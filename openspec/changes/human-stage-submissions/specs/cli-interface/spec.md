## ADDED Requirements

### Requirement: Human stage submit command

The CLI MUST provide `ai-team stage submit <run_id> --stage <id>` with exactly
one result form: `--md <file>`, `--text <markdown>`, `--link <url> --kind
<pr|build|other>`, or `--approve`. It MUST accept optional `--note`,
`--description`, `--actor`, `--role`, and `--target` values. Markdown file input
MUST be read as a regular file within the shared 10 MiB limit. On success the
CLI MUST report the immutable version and SHA-256; invalid, stale, or conflicting
submissions MUST exit nonzero.

#### Scenario: Submit markdown from a file

- **WHEN** the CLI receives a valid pending input approval and a markdown file
- **THEN** it MUST preserve the file's exact bytes, resolve the approval, and
  print the resulting version and SHA-256

#### Scenario: Submit a link or approve

- **WHEN** the CLI receives `--link` and a valid `--kind`, or `--approve`
- **THEN** it MUST submit the configured typed result and optional note through
  the same approval and revision contract
