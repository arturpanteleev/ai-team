## Why

The web dashboard's REST API (`/api/*`) had no origin check at all — only the
WebSocket upgrade path did, and that check (`Origin.Host == r.Host`) doesn't
actually defeat DNS rebinding: during a rebind, a browser's Origin and Host
headers both reflect the attacker's domain and agree with each other, even
though the TCP connection lands on loopback. `cmd/ai-team`'s own fatal
message for non-loopback `--host` says outright that the web UI has no
authentication — bind-to-loopback plus a *correct* origin check is the only
real boundary. Independent audit Finding 4 (High): with a running
`ai-team web`, a malicious webpage the user happens to visit could read
pipeline/run/artifact data via the unprotected REST API, or via rebinding
past the WebSocket check.

## What Changes

- New `sameOriginMiddleware`, applied to every route: rejects any request
  whose Host header isn't a loopback hostname (127.0.0.1/localhost/::1),
  and — when an Origin header is present — rejects it too unless it also
  names a loopback host. This is the actual DNS-rebinding defense: an
  allow-list of what the request is allowed to *claim*, not a comparison
  between two attacker-controlled headers.
- WebSocket's own `CheckOrigin` updated to use the same loopback allow-list
  instead of comparing Origin to Host.

## Capabilities

### Modified Capabilities
- `web-http-server`: strengthens the "Local-only security boundary"
  requirement to cover the REST API (not just WebSocket) and to specify
  loopback-allow-list matching rather than Origin-equals-Host comparison.

## Impact
- `pkg/web/security.go` (new): `isLoopbackHostname`, `sameOriginMiddleware`
- `pkg/web/server.go`: registers the middleware
- `pkg/web/websocket.go`: `CheckOrigin` now delegates to `isLoopbackHostname`
