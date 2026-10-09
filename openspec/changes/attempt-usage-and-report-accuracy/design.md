# Design

## Durable token accounting

Each stage manifest carries an optional `usage` record with `attested`, input
tokens, and output tokens. A missing record means the runtime was not invoked;
a non-attested record means it ran but did not report usage. Resume reads the
verified manifest set once and rebuilds totals from attempts, including
invalidated attempts because their model work still consumed tokens. If any
invoked model attempt is unknown, the run-level aggregate omits token numbers
and sets `tokens_unknown`; it must not publish a partial sum as a total.

The task creation timestamp remains the immutable run-manifest `started_at`.
Final elapsed time uses it across resumes. Each call to the pipeline starts a
new `max_execution_time` context. Human approval events use the latest
`Decision.DecidedAt`, which ends the operator wait independently of when the
worker resumes.

## Report and CLI projection

Replay hydrates attempt checks and artifact records from their manifest before
building final report counts. Resume preserves historical report pages and
regenerates them from those immutable records. Final timestamps are rendered
in UTC. The CLI prints only input, output, and total tokens. An optional
`usage.monthly_subscription_amount` and currency enable a clearly approximate
share: configured monthly amount multiplied by this run's token share among
recorded, fully known runs that finished in the same UTC month. Missing
subscription configuration, incomplete monthly usage, or a zero denominator
returns `оценка недоступна`.

Exact API pricing is not part of this presentation.
