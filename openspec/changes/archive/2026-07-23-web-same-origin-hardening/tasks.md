## 1. Implementation

- [x] 1.1 Add `isLoopbackHostname` + `sameOriginMiddleware` (pkg/web/security.go)
- [x] 1.2 Register middleware router-wide in `NewServer`
- [x] 1.3 Update WebSocket `CheckOrigin` to use the same loopback allow-list

## 2. Verification

- [x] 2.1 Test: hostile Host rejected on REST routes
- [x] 2.2 Test: hostile Origin rejected even with a loopback Host (the actual rebinding scenario)
- [x] 2.3 Test: loopback Host/Origin combinations (with and without port, IPv6) allowed
- [x] 2.4 Test: WebSocket CheckOrigin rejects rebind-style Origin, allows loopback Origin and missing Origin
- [x] 2.5 Fix existing server tests broken by the new middleware (httptest.NewRequest defaults to a non-loopback Host)
- [x] 2.6 Manual end-to-end check against the real built binary: normal request 200, spoofed Host header 403
