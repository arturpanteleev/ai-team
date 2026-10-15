package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// Network MCP endpoint (S12). One stateless Streamable HTTP endpoint,
// POST /mcp, served by the official Go SDK (MCP 2026-07-28; the SDK also
// answers the initialize handshake of 2025-11-25 clients without a session).
//
// Identity: every request carries `Authorization: Bearer <token>`. The token
// is either a personal MCP token (aitm_..., long-lived, hashed in SQLite,
// revocable) or any token the web server's IdentityVerifier already accepts
// (cloud HMAC token, the local loopback token). The principal is then resolved
// exactly as for a browser sign-in (resolveTeamPrincipal), so team membership,
// roles and revocation apply on every call. The browser cookie is never read
// on this route.

const (
	mcpPath = "/mcp"
	// mcpMaxRequestBody bounds one JSON-RPC request: read tools take small
	// arguments, a submitted stage result or template may be a few MiB.
	mcpMaxRequestBody = 4 << 20
	mcpServerName     = "ai-team"
)

const mcpInstructions = `ai-team: process-gated delivery of tasks through a configurable template. ` +
	`Everything here acts as the person who owns the access token, with exactly that person's roles; ` +
	`you cannot see or do more than they can in the web UI. Start with whoami, then list_projects and list_tasks. ` +
	`Actions (start_task, confirm_stage, decide_approval, ...) need an idempotency_key: resending a call with the same key never repeats the action. ` +
	`Confirmations and decisions need the exact subject_hash you reviewed (read it from get_task); an override of a failed gate needs the administrator right and a reason; ` +
	`a task action returns at once with the task ID - follow it with get_task, get_task_gates and list_task_events. ` +
	`Lists are paginated (limit/offset, or next_cursor for events); results larger than 256 KiB are cut and ` +
	`continue with byte_offset=next_byte_offset. Errors are returned as {"error","code","status"} with isError=true.`

type mcpSessionContextKey struct{}
type mcpIdentityContextKey struct{}

// mcpIdentity is the authenticated caller of one MCP request.
type mcpIdentity struct {
	Session browserSession
	Host    string
	TokenID string // personal token ID; empty for other bearer kinds
	// UserAgent names the client (Claude Code, Codex, ...) for the audit trail.
	UserAgent string
}

func mcpIdentityFromContext(ctx context.Context) (mcpIdentity, bool) {
	identity, ok := ctx.Value(mcpIdentityContextKey{}).(mcpIdentity)
	return identity, ok
}

// WithMCPAllowedOrigins lists browser origins (scheme://host[:port]) that may
// call /mcp in addition to the server's own origin. Requests without an Origin
// header (CLI agents, server-side connectors) are always allowed; a request
// with any other Origin is rejected with 403, as the MCP transport
// specification requires.
func WithMCPAllowedOrigins(origins ...string) ServerOption {
	return func(server *Server) { server.mcpAllowedOrigins = append(server.mcpAllowedOrigins, origins...) }
}

// normalizeMCPOrigins validates and canonicalizes the configured allow-list.
func normalizeMCPOrigins(origins []string) ([]string, error) {
	result := make([]string, 0, len(origins))
	for _, raw := range origins {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Opaque != "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("MCP allowed origin %q must look like https://host[:port]", raw)
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return nil, fmt.Errorf("MCP allowed origin %q must use http or https", raw)
		}
		host, port, ok := effectiveAuthority(parsed.Host, scheme)
		if !ok {
			return nil, fmt.Errorf("MCP allowed origin %q has an invalid host", raw)
		}
		result = append(result, scheme+"://"+host+":"+port)
	}
	return result, nil
}

// mcpOriginAllowed implements the Origin policy for /mcp: no Origin is fine
// (non-browser client); the server's own origin and configured origins pass;
// everything else, including a malformed or "null" Origin, is refused.
func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if originMatchesRequest(r, origin) {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	host, port, ok := effectiveAuthority(parsed.Host, scheme)
	if !ok {
		return false
	}
	canonical := scheme + "://" + host + ":" + port
	for _, allowed := range s.mcpAllowedOrigins {
		if allowed == canonical {
			return true
		}
	}
	return false
}

// exceptMCP applies a router-wide middleware to everything but /mcp, which
// carries its own Origin and authentication rules.
func exceptMCP(middleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == mcpPath {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// mcpGuard enforces the transport-level rules before the SDK sees a request:
// Host (loopback only in local mode, anything non-empty in cloud mode) and
// Origin, then bearer authentication. Failures never say which check failed
// for credentials.
func (s *Server) mcpGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Host) == "" || ((s.authenticator == nil || s.localAuth) && !isLoopbackHostname(r.Host)) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !s.mcpOriginAllowed(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if s.authenticator == nil {
			http.Error(w, "MCP requires an authenticated server", http.StatusServiceUnavailable)
			return
		}
		session, tokenID, err := s.mcpAuthenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ai-team-mcp"`)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "invalid or missing bearer token", http.StatusUnauthorized)
			return
		}
		identity := mcpIdentity{Session: session, Host: r.Host, TokenID: tokenID, UserAgent: auditClientName(r.UserAgent())}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpIdentityContextKey{}, identity)))
	})
}

var errMCPUnauthorized = errors.New("mcp: unauthorized")

// mcpAuthenticate turns the Authorization header into a team-resolved
// principal. Unknown, expired, revoked and malformed credentials are one error.
func (s *Server) mcpAuthenticate(r *http.Request) (browserSession, string, error) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") || len(fields[1]) > 4096 {
		return browserSession{}, "", errMCPUnauthorized
	}
	secret := fields[1]
	now := s.clock()
	var principal cloudidentity.Principal
	var tokenID string
	expires := now.Add(time.Minute)
	if strings.HasPrefix(secret, store.MCPTokenPrefix) {
		if s.localAuth {
			// The loopback server has one fixed identity and no team registry:
			// it is reached with the local token, not personal tokens.
			return browserSession{}, "", errMCPUnauthorized
		}
		token, err := s.store.MCPTokenByRaw(secret, now)
		if err != nil {
			return browserSession{}, "", errMCPUnauthorized
		}
		// The stored token names an actor only; roles come from the live team
		// registry below, so role changes and revocation apply immediately. The
		// placeholder role is replaced by resolveTeamPrincipal.
		principal, err = cloudidentity.NewPrincipal(token.ActorID, []cloudidentity.Role{cloudidentity.RoleDeveloper})
		if err != nil {
			return browserSession{}, "", errMCPUnauthorized
		}
		tokenID, expires = token.ID, token.ExpiresAt
	} else {
		var err error
		principal, err = s.authenticator.Verify(secret)
		if err != nil {
			return browserSession{}, "", errMCPUnauthorized
		}
	}
	// No bootstrap: an MCP request must never create the first team member.
	principal, epoch, err := s.resolveTeamPrincipalOpts(principal, false)
	if err != nil {
		return browserSession{}, "", errMCPUnauthorized
	}
	csrf, err := randomToken()
	if err != nil {
		return browserSession{}, "", errMCPUnauthorized
	}
	return browserSession{CSRFToken: csrf, Principal: principal, SessionEpoch: epoch, AuthMethod: "mcp", ExpiresAt: expires}, tokenID, nil
}

// newMCPHandler builds the SDK server with every registered operation and
// wraps it in the guard. The SDK server is shared; identity travels in the
// request context, never in server state.
func (s *Server) newMCPHandler() http.Handler {
	sdk := mcp.NewServer(&mcp.Implementation{Name: mcpServerName, Title: "ai-team", Version: mcpServerVersion()},
		&mcp.ServerOptions{Instructions: mcpInstructions,
			// Stateless endpoint: no server-initiated messages, so no listChanged
			// promises and no logging channel.
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}, Resources: &mcp.ResourceCapabilities{}}})
	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	registerMCPActionTools(registry)
	registry.register(sdk, s)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: mcpMaxRequestBody,
	})
	return s.mcpGuard(streamable)
}

// auditClientName keeps a client's User-Agent short and single-line so it can
// sit in an audit record.
func auditClientName(agent string) string {
	agent = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(agent))
	if len(agent) > 120 {
		agent = agent[:120]
	}
	return strings.ToValidUTF8(agent, "")
}

func mcpServerVersion() string {
	if revision := readBuildRevision().Revision; len(revision) >= 7 {
		return revision[:7]
	}
	return "dev"
}
