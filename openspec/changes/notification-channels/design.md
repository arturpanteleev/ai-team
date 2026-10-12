# Design: notification channels

`pkg/notifier.Channel` is the adapter boundary (`Name`, `Send`). Webhook and
Slack share one HTTPS poster: the URL must be absolute `https` without
userinfo, each delivery has a 10s timeout, and the client's redirect policy
allows only same-scheme, same-host redirects. Transport errors are reduced to
a class (`HTTP 503`, `request timed out`, `connection failed (dial)`,
`redirect to a different host was rejected`, `TLS verification failed`) so the
URL, which may itself be a credential (Slack), never reaches events or logs.
E-mail parses one `smtp://` / `smtps://` URL, uses STARTTLS when offered, lets
`net/smtp` refuse PLAIN auth without TLS to non-loopback hosts and reports SMTP
failures by step and reply code only.

`notifier.Dispatcher` holds routes (channel + subscription), the template's
stage→function map and the optional web base URL. The web server consumes the
event store with its own integration cursor `notifier` (same worker loop as
the Jira tracker) and calls `Dispatch` per event. For each matching channel it
checks `notification_deliveries(channel, event_id)`; an existing row means the
event was already handled. After a successful send it records `delivered`.
After a failure it first persists `integration_error` (`integration=notifier`,
`channel`, sanitized `error`) and then records `failed`, so a broken channel is
not retried forever and other channels are unaffected. If either write fails
the event stays pending; already delivered channels are skipped on replay.
`integration_error` events produced by the notifier are never dispatched.

The only duplicate window is a crash between a channel accepting a delivery and
the delivery row being committed. Webhooks carry `Idempotency-Key` and
e-mails a stable `Message-ID` derived from the event cursor for that case.

Alternatives considered: deduplicating only by the integration cursor (as the
tracker does) loses idempotency when the cursor is rewound or when one of
several channels fails mid-event; per-channel cursors multiply worker loops
without adding guarantees over the delivery log.
