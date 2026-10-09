# Human stage result submission

## Summary

Add a typed submission path for human-executed process stages. A person can
submit markdown from a file or text, submit a categorized HTTP(S) link, or
approve a stage with optional text. Each accepted submission receives an
immutable stage-scoped version and exact-content SHA-256, then resolves the
stage's input approval.

## Motivation

Human input approvals already bind a result to its stage and workflow subject,
but the CLI and Web API do not provide one consistent submission contract or
retain a versioned result history. Submission retries and concurrent requests
must not replace previously accepted bytes, and resumed human output must remain
part of the immutable attempt evidence.

## Scope

- Add `ai-team stage submit` for markdown files/text, links, and approvals.
- Add `POST /api/runs/{runID}/stages/{stageID}/submit` and stage result history
  through the existing artifact revision API.
- Store exact content, description, stage, result kind, link kind, approval ID,
  version, and SHA-256 in immutable revisions; bind the same version/hash to the
  resolved approval decision.
- Validate markdown size/encoding, URL schemes, and configured link kind.
- Record a controller-only `description_missing` warning when a resumed human
  result has an empty description and bind that warning to the active human
  attempt and input approval.

## Risks

Approval records may contain up to 10 MiB of submitted markdown, so serialized
approval storage and API envelopes need independent bounded limits. Concurrent
submissions must use append-only revision creation and approval-store
compare-and-write behavior so they cannot silently overwrite one another.
