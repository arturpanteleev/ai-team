package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

var routeParamPattern = regexp.MustCompile(`\{[^}]*\}|\*`)

// normalizeRoute makes a REST pattern ("/api/runs/{runID}") and a tool path
// ("/api/runs/{run_id}") comparable.
func normalizeRoute(path string) string { return routeParamPattern.ReplaceAllString(path, "{}") }

// walkedRoutes lists "METHOD pattern" of every route a router serves.
func walkedRoutes(t *testing.T, router chi.Routes) map[string]bool {
	t.Helper()
	routes := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// parityServers builds the router in every configuration that changes the set
// of routes: plain cloud mode, single sign-on, and a served frontend.
func parityServers(t *testing.T) []*Server {
	t.Helper()
	plain := newMCPEnv(t).srv
	sso := newSSOFixture(t).srv
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	artifacts := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	withFrontend, err := NewServer(":memory:", dist, artifacts, WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withFrontend.Close() })
	return []*Server{plain, sso, withFrontend}
}

func matrixEntryFor(method, route string) (mcpRouteParity, bool) {
	for _, entry := range mcpParityMatrix {
		if entry.Pattern == route && (entry.Method == method || entry.Method == "*") {
			return entry, true
		}
	}
	return mcpRouteParity{}, false
}

// TestMCPParityMatrix is the S13 gate: every route the real router serves has
// a row (an MCP tool, or the reason transport authorization replaces it), every
// row names a real route and a real tool that reaches that very route, and no
// tool is left outside the matrix. A route added later without a row fails here.
func TestMCPParityMatrix(t *testing.T) {
	servers := parityServers(t)
	walked := map[string]bool{}
	for _, srv := range servers {
		for route := range walkedRoutes(t, srv.router) {
			walked[route] = true
		}
	}

	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	registerMCPActionTools(registry)
	tools := map[string]mcpOperation{}
	for _, operation := range registry.operations {
		tools[operation.Name] = operation
	}

	// 1. Every served route is in the matrix.
	for route := range walked {
		method, pattern, _ := strings.Cut(route, " ")
		if _, ok := matrixEntryFor(method, pattern); !ok {
			t.Errorf("route %s is served but has no row in mcpParityMatrix: add the MCP tool that performs it or the reason it needs none", route)
		}
	}

	seen := map[string]bool{}
	used := map[string]bool{}
	for _, entry := range mcpParityMatrix {
		key := entry.Method + " " + entry.Pattern
		if seen[key] {
			t.Errorf("duplicate matrix row %s", key)
		}
		seen[key] = true

		// 2. Every row is a served route (in some configuration).
		served := false
		for route := range walked {
			method, pattern, _ := strings.Cut(route, " ")
			if pattern == entry.Pattern && (entry.Method == "*" || entry.Method == method) {
				served = true
			}
		}
		if !served {
			t.Errorf("matrix row %s names a route no server registers (stale?)", key)
		}

		// 3. A row is a tool xor an explained exception.
		if (entry.Tool == "") == (entry.Kind == "") {
			t.Errorf("row %s must name a tool or an exception kind, exactly one", key)
			continue
		}
		if entry.Tool == "" {
			switch entry.Kind {
			case mcpExceptTransportAuth, mcpExceptTransport, mcpExceptStatic:
			default:
				t.Errorf("row %s: unknown exception kind %q", key, entry.Kind)
			}
			if len(entry.Reason) < 40 {
				t.Errorf("row %s: an exception needs a real explanation, got %q", key, entry.Reason)
			}
			continue
		}
		if entry.Reason != "" || entry.Kind != "" {
			t.Errorf("row %s: a tool row carries no exception text", key)
		}

		// 4. The tool exists and does this operation on this very route.
		operation, ok := tools[entry.Tool]
		if !ok {
			t.Errorf("row %s names tool %q which is not registered", key, entry.Tool)
			continue
		}
		if !entry.Alias {
			used[entry.Tool] = true
			if operation.Method != entry.Method {
				t.Errorf("row %s: tool %s uses method %s", key, entry.Tool, operation.Method)
			}
			if normalizeRoute(operation.Path) != normalizeRoute(entry.Pattern) {
				t.Errorf("row %s: tool %s calls %s", key, entry.Tool, operation.Path)
			}
		} else if operation.Method != entry.Method {
			t.Errorf("alias row %s: tool %s uses method %s", key, entry.Tool, operation.Method)
		}
	}

	// 5. No tool outside the matrix, and every tool resolves, in the real
	// router, to the pattern its row claims.
	for name, operation := range tools {
		if !used[name] {
			t.Errorf("tool %s is not the primary tool of any matrix row", name)
		}
		sample := operation.Path
		for _, placeholder := range pathPlaceholders(operation.Path) {
			sample = strings.ReplaceAll(sample, "{"+placeholder+"}", "x")
		}
		resolved := servers[0].router.Find(chi.NewRouteContext(), operation.Method, sample)
		if resolved == "" {
			t.Errorf("tool %s: %s %s is not routed", name, operation.Method, sample)
			continue
		}
		if entry, ok := matrixEntryFor(operation.Method, resolved); !ok || entry.Tool != name {
			t.Errorf("tool %s resolves to route %s which the matrix assigns to %q", name, resolved, entry.Tool)
		}
	}
}

// The credential and session routes must stay out of reach of every tool, and
// the router itself must refuse an MCP identity there, so a registry mistake
// cannot let an MCP token mint tokens.
func TestMCPTokenAndSessionRoutesAreNeverTools(t *testing.T) {
	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	registerMCPActionTools(registry)
	for _, operation := range registry.operations {
		for _, forbidden := range []string{"/api/mcp/", "/api/session", "/api/team/activate", "/api/auth/oidc"} {
			if strings.HasPrefix(operation.Path, forbidden) {
				t.Errorf("tool %s reaches %s", operation.Name, operation.Path)
			}
		}
	}
	env := newMCPEnv(t)
	session, _, err := env.srv.mcpAuthenticate(mcpTestRequest(t, env, "owner@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	identity := mcpIdentity{Session: session, Host: "127.0.0.1"}
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/api/mcp/tokens"}, {http.MethodPost, "/api/mcp/tokens"}, {http.MethodDelete, "/api/mcp/tokens/abc"}} {
		outcome := env.srv.mcpDispatch(t.Context(), identity, call.method, call.path, nil, []byte(`{"label":"x"}`), nil)
		if outcome.status != http.StatusForbidden {
			t.Errorf("%s %s with an MCP identity = %d %s, want 403", call.method, call.path, outcome.status, outcome.body)
		}
	}
}

// docs/reference/mcp.md carries the matrix for people; it must list every row
// and every tool, so the document cannot silently fall behind the code.
func TestMCPParityMatrixIsInTheReferenceDocument(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "mcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, entry := range mcpParityMatrix {
		if !strings.Contains(doc, "`"+entry.Pattern+"`") {
			t.Errorf("docs/reference/mcp.md does not mention route %s", entry.Pattern)
		}
		if entry.Tool != "" && !strings.Contains(doc, "`"+entry.Tool+"`") {
			t.Errorf("docs/reference/mcp.md does not mention tool %s", entry.Tool)
		}
	}
	registry := newMCPRegistry()
	registerMCPReadTools(registry)
	registerMCPActionTools(registry)
	for _, operation := range registry.operations {
		if !strings.Contains(doc, "`"+operation.Name+"`") {
			t.Errorf("docs/reference/mcp.md does not mention tool %s", operation.Name)
		}
	}
}
