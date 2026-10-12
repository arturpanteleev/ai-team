# Browser stage result submission (B-63)

## Proposal

Let a person submit every human stage result type from the task page: a
Markdown editor with preview and file upload, a link form that uses the stage's
link kind and validates the URL, and an approve with optional text. The form
warns about an empty “Что сделано” and keeps an unsent draft in the browser so
a page reload does not lose work.

## Motivation

B-31 already accepts `md`, `link`, and `approve` submissions through the CLI and
`POST /api/runs/{runID}/stages/{stageID}/results`, and B-62 added a minimal
form to the task page. That form had no preview or upload, sent a free link
kind the server rejects unless it matches the template, gave no early feedback
on URL or size limits, and dropped everything on reload.

## Scope

- Reuse the existing results endpoint and server validation; no API changes.
- Markdown: editor/preview toggle and `.md` upload (UTF-8, at most 10 MiB, the
  `humanartifact.MaxContentBytes` limit measured in bytes).
- Link: URL plus the pinned stage `link_kind`; only HTTP(S), no credentials,
  at most 16 KiB; a non-blocking hint when a `pr` link does not look like a PR.
- Approve: optional confirmation text sent as `note`.
- “Что сделано”: warning when empty; submission still goes to the server,
  which records `description_missing`.
- Draft per run and stage in `localStorage`, guarded by `try/catch`.

## Acceptance criteria

- All three result types can be submitted from the browser.
- A draft survives a page reload and is removed only after the server accepts
  the submission.
- The page works when `localStorage` is unavailable or full.
- `GOTOOLCHAIN=go1.26.9 make verify` passes.
