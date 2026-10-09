## MODIFIED Requirements

### Requirement: Human executor stages collect typed results

A stage with `executor: human` MUST stop for a typed `input` approval bound to
the stage ID, result type, output name/path, declared input digests, and current
candidate identity when present. `result: approve` MUST collect an explicit
`approve` or `reject` action; `result: md` and `result: link` MUST collect a
`submit` or `reject` action. Markdown submissions MUST be non-empty valid UTF-8
and at most 10 MiB. Link submissions MUST use one of the configured kinds
`pr`, `build`, or `other`, and their URL MUST use HTTP(S) with a host. Approval
text is optional and bounded to 16 KiB. `--approve-gates` MUST NOT synthesize a
human result. The request MUST leave the stage without a started attempt; after
a valid submission, the controller MUST resume that same stage, publish the
human result as its attempt output and manifest, and then apply the ordinary
graph edge and its independent confirmation policy.

For a versioned submission, the immutable attempt manifest MUST record the
submission version, exact-content SHA-256, result kind, link kind when present,
and description. Markdown and link outputs MUST have the same SHA-256 as the
submitted result bytes; the attempt manifest's own digest MUST remain bound to
the `attempt_finished` event.

#### Scenario: Human stage pauses and resumes with markdown

- **WHEN** an authorized actor submits markdown for the exact subject
- **THEN** the same stage MUST resume with the submitted bytes as its output,
  record the actor and `executor: human`, and include the output in the
  immutable attempt manifest

#### Scenario: Invalid markdown or link

- **WHEN** markdown exceeds 10 MiB, is empty or invalid UTF-8, a link kind does
  not match the configured stage, or a URL uses a scheme other than HTTP(S)
- **THEN** the submission MUST be rejected without resolving the input approval

#### Scenario: Human submission has no description

- **WHEN** a typed human result is resumed with an empty `description`
- **THEN** the controller MUST emit the `description_missing` warning required
  by `human-approvals` and continue the stage
