# Task-oriented Web API

## Why

The Web API exposes the older numeric pipeline projection, while the task UI
needs a stable run identity, the pinned template, stage actions, result history,
event feeds, and flow metrics through one `/api/runs` resource.

## Scope

Add the task-oriented routes in §10 of `TZ.md`, normalize API errors to JSON,
derive actor identity and authorization from the authenticated session, and
support request-key replay for writes. Read/list/event/metrics routes are
implemented in this tranche. Stage executor, skip/return, agent/check/grill,
and delivery-plan actions use optional typed controller interfaces so the Web
layer can be integrated with the in-flight B-32/B-34/B-41/B-42 engine changes
without duplicating controller behavior.

## Acceptance criteria

- Task list/detail/templates/result history and run/global event feeds use
  stable run IDs, pagination, filters, and the run's pinned template.
- Flow metrics expose lead/cycle time, stage counts and durations, action
  counts, usage completeness, and CSV export without treating unknown usage as
  zero.
- Writes preserve the existing session, CSRF, RBAC, and server-derived actor
  semantics; retries with the same request key replay the original response.
- State conflicts return JSON 409 with the currently allowed actions.
- All action routes have controller-backed success, invalid-state, repeat, and
  unauthenticated regressions before this change is complete.
- Pinned `GOTOOLCHAIN=go1.26.9 make verify` passes on the final commit.
