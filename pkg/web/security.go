package web

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	switch host {
	case "127.0.0.1", "::1":
		return true
	}
	return strings.EqualFold(host, "localhost")
}

// effectiveAuthority parses a Host or URL authority and supplies the scheme's
// default port when none was written. URL origins omit their default port, so
// comparing raw Host strings would reject equivalent origins and can also
// accidentally treat different ports as same-origin.
func effectiveAuthority(authority, scheme string) (hostname, port string, ok bool) {
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", false
	}
	hostname = parsed.Hostname()
	if hostname == "" {
		return "", "", false
	}
	hostname = strings.ToLower(hostname)
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	}
	port = parsed.Port()
	if port == "" {
		switch strings.ToLower(scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", "", false
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false
		}
		port = strconv.Itoa(n)
	}
	return hostname, port, true
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// originMatchesRequest checks the browser's Origin against the actual request
// origin, including the scheme and effective (defaulted) port.
func originMatchesRequest(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Opaque != "" || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != requestScheme(r) {
		return false
	}
	originHost, originPort, ok := effectiveAuthority(u.Host, scheme)
	if !ok {
		return false
	}
	requestHost, requestPort, ok := effectiveAuthority(r.Host, requestScheme(r))
	return ok && originHost == requestHost && originPort == requestPort
}

// sameOriginMiddleware restricts local requests to loopback Host values and
// requires a present browser Origin to match the request's full origin.
// Applied to every route, it keeps the fixed allow-list that prevents DNS
// rebinding while also rejecting another local service's port.
func sameOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHostname(r.Host) {
			http.Error(w, "запрещённый Host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !originMatchesRequest(r, origin) {
				http.Error(w, "запрещённый Origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func authenticatedOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Host) == "" {
			http.Error(w, "пустой Host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !originMatchesRequest(r, origin) {
				http.Error(w, "запрещённый Origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
