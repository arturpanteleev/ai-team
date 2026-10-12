# Notification channels (B-81) — tasks

- [x] Add `Channel`, `Subscription`, `Dispatcher` and webhook, Slack, e-mail
  channels with sanitized errors, HTTPS-only destinations and same-host
  redirects.
- [x] Add strict `notifications` template configuration with env-only
  destinations, known event types and template functions.
- [x] Consume the durable event cursor in the web server, record per-channel
  deliveries and `integration_error` without affecting task outcomes.
- [x] Add httptest, fake SMTP, reconnect replay, function filter, failure and
  redirect regressions.
- [x] Update OpenSpec, architecture, configuration and security reference.
- [x] Run `GOTOOLCHAIN=go1.26.9 make verify` on the final tree.
