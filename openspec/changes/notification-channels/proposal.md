# Notification channels (B-81)

## Proposal

Add outbound task notifications on top of `pkg/notifier`: a generic HTTPS
webhook, a Slack incoming webhook and SMTP e-mail. Each channel subscribes to
task event types and, optionally, to template stage functions. Delivery runs in
the web server over the durable SQLite event stream, so a reconnect or restart
replays missed events without notifying twice. Destinations and credentials
come only from the server environment; delivery failures become durable
`integration_error` warnings and never change a task outcome.
