## Context

The server can only ever be started bound to a loopback address —
`cmd/ai-team`'s `cmdWeb` fatals on any other `--host` before `ListenAndServe`
is ever called. Given that invariant, the only way a browser can reach this
server at all is via a URL whose hostname resolves to loopback. DNS
rebinding is the one attack that breaks the naive assumption that "the
request reached us, so it must be legitimate": a page can register a domain
with a short-TTL DNS record, have the browser's `fetch()` resolve it to
loopback *after* the page has already loaded from a public IP, and the
resulting request's Host/Origin headers will still say the attacker's domain
— they just happen to route to 127.0.0.1 now.

## Goals / Non-Goals

**Goals:** every route — REST and WebSocket — rejects any request that
doesn't claim to be loopback, checking both Host (always present) and Origin
(when present, for browser requests).

**Non-Goals:** authentication. The security model stays "loopback-only, no
auth" as documented; this only closes the specific gap where the origin
check didn't actually enforce that model correctly.

## Decisions

Check against a fixed allow-list (`127.0.0.1`, `localhost`, `::1`) rather
than comparing Origin to Host. The two-header comparison is exactly what
DNS rebinding defeats — both headers move together. An allow-list of
literal, non-attacker-controllable strings is what actually works.

Applied as router-wide middleware (`chi`'s `Use`) rather than per-handler,
so new routes get the check automatically and it can't be forgotten on one
handler.

Did not plumb the actual configured port through into the check — validating
only the *hostname* portion (ignoring port) is sufficient, since the server
only ever listens on a loopback interface in the first place; the port a
client used to reach it is irrelevant to whether the claimed hostname is
trustworthy.

## Risks / Trade-offs

None identified — this only rejects requests that were already indefensible
under the documented threat model (loopback-only, no auth). No legitimate
client (the bundled frontend, `curl` without a spoofed Host, the CLI itself)
is affected.
