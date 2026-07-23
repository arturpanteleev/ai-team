package web

import (
	"net"
	"net/http"
	"net/url"
)

// isLoopbackHostname reports whether host (a Host or Origin hostname, with
// or without a port) is one of the forms the CLI restricts binding to
// (cmd/ai-team's cmdWeb rejects any --host other than these). Checking the
// claimed Host/Origin string against this fixed set — rather than comparing
// Origin's host to the request's own Host header — is what actually defeats
// DNS rebinding: during a rebind, the browser's Origin and Host both reflect
// the attacker's domain and agree with each other, even though the
// connection lands on loopback. Only an explicit allow-list catches that.
func isLoopbackHostname(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// sameOriginMiddleware rejects any request whose Host header, or whose
// Origin header (when present), does not resolve to a loopback hostname.
// Applied to every route: the REST API previously had no origin check at
// all, and the WebSocket upgrade path's own CheckOrigin compared Origin to
// Host rather than to a fixed loopback allow-list (see websocket.go).
func sameOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHostname(r.Host) {
			http.Error(w, "запрещённый Host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !isLoopbackHostname(u.Host) {
				http.Error(w, "запрещённый Origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
