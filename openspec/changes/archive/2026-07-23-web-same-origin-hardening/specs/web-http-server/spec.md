## MODIFIED Requirements

### Requirement: Local-only security boundary
CLI MUST по умолчанию bind web server к `127.0.0.1`. Every route — REST API
and WebSocket alike — MUST reject any request whose Host header, or whose
Origin header when present, does not resolve to a loopback hostname
(`127.0.0.1`, `localhost` or `::1`). Comparing Origin to Host is not
sufficient, since DNS rebinding makes both headers agree with each other
while still reflecting an attacker-controlled domain.

#### Scenario: Cross-origin browser request
- **WHEN** запрос приходит с постороннего browser origin
- **ТОГДА** сервер MUST NOT добавлять permissive CORS headers
- **AND** WebSocket upgrade MUST быть отклонён

#### Scenario: Rebind-style REST request
- **WHEN** an HTTP request's Host header names a non-loopback hostname (regardless of what IP it actually routed through)
- **THEN** every route, including the REST API, MUST reject it with 403

#### Scenario: Rebind-style Origin with a loopback Host
- **WHEN** a request's Host header is loopback but its Origin header names a non-loopback hostname
- **THEN** the request MUST be rejected with 403
