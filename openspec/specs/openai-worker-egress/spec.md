# OpenAI Worker Egress Specification

## Purpose

Provide a narrowly scoped remote-model path for an opt-in Linux bubblewrap
worker while keeping direct worker networking disabled. The first supported
provider is OpenAI, selected by the project owner on 2026-10-07.

## Requirements

### Requirement: OpenAI-only controller egress

A bubblewrap worker MUST keep its network namespace isolated and MUST route
HTTPS CONNECT through a controller-owned per-invocation Unix socket. The
controller MUST accept only the exact destination `api.openai.com:443`, require
a random per-invocation capability, resolve the hostname in the controller,
and connect only to public unicast addresses returned for that hostname. It
MUST reject every other host, port, malformed destination, and missing or
incorrect capability. DNS results in special-purpose, reserved, private,
shared-address, documentation, benchmarking, transition, loopback, link-local,
or other non-public ranges MUST be rejected for both IPv4 and IPv6. Public
unicast addresses remain valid. The deny-list is conservative and does not
claim to validate BGP reachability. The proxy MUST NOT log capability values,
HTTP headers, request bodies, or CONNECT destinations.

#### Scenario: OpenAI endpoint is tunneled

- **WHEN** an OpenCode subprocess uses `HTTPS_PROXY` to CONNECT to
  `api.openai.com:443`
- **THEN** a namespace-local bridge carries the CONNECT stream over the scoped
  controller Unix socket
- **AND** the controller opens only the allowlisted endpoint
- **AND** TLS remains end-to-end between the model client and the upstream

#### Scenario: Other destination is rejected

- **WHEN** a worker requests CONNECT to any hostname other than
  `api.openai.com` or any port other than `443`
- **THEN** the bridge or controller rejects the request before opening an
  upstream connection

#### Scenario: Direct worker IP egress remains unavailable

- **WHEN** the worker attempts a TCP connection without using the proxy
- **THEN** host loopback and external IP connections remain unreachable

### Requirement: Proxy environment is controller-owned

When the controller provisions the OpenAI bridge, the OpenCode subprocess MUST
receive the bridge URL through `HTTPS_PROXY` and `HTTP_PROXY`, with
`NO_PROXY=localhost,127.0.0.1,::1` for OpenCode's local TUI server. User-provided
proxy variables MUST NOT override these values in the sandboxed harness.

#### Scenario: Local OpenCode server bypasses proxy

- **WHEN** OpenCode connects to its local HTTP server
- **THEN** `NO_PROXY` bypasses the egress bridge for localhost addresses

### Requirement: Test evidence is not a production provider round-trip

A fake upstream integration probe MUST verify the CONNECT tunnel, destination
denials, and direct-network denial. Passing this probe MUST NOT be described as
a successful request to OpenAI or as proof that every installed OpenCode
version performs a model request through the environment proxy. Real-provider
verification requires a separately authorized, secret-safe smoke test using a
supported OpenCode version and an explicitly configured OpenAI key. The
controller's isolated HOME does not inherit OpenCode's `auth.json`; standard
OpenAI API-key auth is passed only through the explicit
`AI_TEAM_WORKER_ENV_ALLOW=OPENAI_API_KEY` setting. ChatGPT/Codex OAuth endpoints
and alternate/custom base URLs are outside the allowlist.

#### Scenario: Fake upstream probe passes

- **WHEN** the Linux integration probe receives a response from a fake TLS
  upstream through the controller socket
- **THEN** the implementation may claim only that the constrained proxy path
  works against the fake upstream
- **AND** real OpenAI provider integration remains unverified
