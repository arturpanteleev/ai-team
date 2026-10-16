package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// pagedJira is a Jira double that splits search results and changelogs into
// small pages, the way a real instance does for busy projects.
type pagedJira struct {
	mu        sync.Mutex
	legacy    bool
	issues    []string // issue keys returned by search, in order
	histories map[string][]map[string]any
	embedCut  int // histories embedded per issue (0 = all)
	pageSize  int
	paths     []string
	authz     []string
	loop      bool // search never ends: every page has a next token

	noTotal       bool // embedded changelog and /changelog omit total
	noChangelog   bool // /changelog answers 404 (old Data Center)
	shiftOnUpdate bool // ORDER BY updated rotates the result between requests
	searches      int
}

func (p *pagedJira) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, r.URL.Path)
	p.authz = append(p.authz, r.Header.Get("Authorization"))
	q := r.URL.Query()
	switch {
	case strings.HasSuffix(r.URL.Path, "/myself"):
		_, _ = w.Write([]byte(`{"accountId":"bot-1"}`))
	case strings.HasSuffix(r.URL.Path, "/changelog"):
		if p.noChangelog {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		all := p.histories[parts[len(parts)-2]]
		start, _ := strconv.Atoi(q.Get("startAt"))
		end := min(start+p.pageSize, len(all))
		body := map[string]any{"startAt": start, "maxResults": p.pageSize, "values": all[start:end]}
		if !p.noTotal {
			body["total"] = len(all)
			body["isLast"] = end >= len(all)
		}
		_ = json.NewEncoder(w).Encode(body)
	case strings.HasSuffix(r.URL.Path, "/search/jql") || strings.HasSuffix(r.URL.Path, "/search"):
		if p.legacy == strings.HasSuffix(r.URL.Path, "/search/jql") {
			http.NotFound(w, r)
			return
		}
		order := append([]string(nil), p.issues...)
		if p.shiftOnUpdate && strings.Contains(q.Get("jql"), "ORDER BY updated") {
			// An issue updated between two pages moves to the end of the list.
			p.searches++
			moved := order[0]
			order = append(order[1:], moved)
			for i := 1; i < p.searches%len(order); i++ {
				order = append(order[1:], order[0])
			}
		} else if strings.Contains(q.Get("jql"), "ORDER BY key") {
			sort.Strings(order)
		}
		start := 0
		if p.legacy {
			start, _ = strconv.Atoi(q.Get("startAt"))
		} else if tok := q.Get("nextPageToken"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+p.pageSize, len(order))
		items := []map[string]any{}
		for _, key := range order[start:end] {
			hs := p.histories[key]
			total := len(hs)
			if p.embedCut > 0 && len(hs) > p.embedCut {
				hs = hs[:p.embedCut]
			}
			changelog := map[string]any{"total": total, "histories": hs}
			if p.noTotal {
				changelog = map[string]any{"histories": hs}
			}
			items = append(items, map[string]any{"key": key, "changelog": changelog})
		}
		body := map[string]any{"issues": items}
		switch {
		case p.legacy:
			body["total"] = len(order)
			body["startAt"] = start
		case end < len(order) || p.loop:
			body["nextPageToken"] = strconv.Itoa(end % max(len(order), 1))
			body["isLast"] = false
		default:
			body["isLast"] = true
		}
		_ = json.NewEncoder(w).Encode(body)
	default:
		http.NotFound(w, r)
	}
}

func pagedHistory(id int) map[string]any {
	created := time.Now().UTC().Add(time.Second).Add(time.Duration(id) * time.Millisecond)
	return map[string]any{
		"id": strconv.Itoa(id), "created": created.Format("2006-01-02T15:04:05.000-0700"),
		"author": map[string]any{"accountId": "acc-person"},
		"items":  []map[string]any{{"field": "status", "fromString": "A", "toString": fmt.Sprintf("S%d", id)}},
	}
}

func (p *pagedJira) adapter(t *testing.T, auth string) *JiraAdapter {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	return NewJiraAdapter(config.JiraConfig{BaseURL: server.URL, Email: "bot@example.test", TokenEnv: "T", APIVersion: 3, Auth: auth}, "secret-pat", server.Client())
}

func pagedCursor() string { return formatJiraCursor(time.Now().UnixMilli()-5000, 0) }

func TestJiraChangesFollowSearchPagesCloudAndServer(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			mock := &pagedJira{legacy: legacy, pageSize: 2, histories: map[string][]map[string]any{}}
			for i := 1; i <= 5; i++ {
				key := fmt.Sprintf("APP-%d", i)
				mock.issues = append(mock.issues, key)
				mock.histories[key] = []map[string]any{pagedHistory(i)}
			}
			page, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: mock.issues})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Changes) != 5 {
				t.Fatalf("all 5 issues over 3 pages must be read, got %d: %+v", len(page.Changes), page.Changes)
			}
		})
	}
}

func TestJiraChangesReadFullChangelogWhenEmbeddedOneIsCut(t *testing.T) {
	mock := &pagedJira{pageSize: 2, embedCut: 2, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
	for i := 1; i <= 5; i++ {
		mock.histories["APP-1"] = append(mock.histories["APP-1"], pagedHistory(i))
	}
	page, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 5 || page.Changes[4].To != "S5" {
		t.Fatalf("the newest histories beyond the embedded cut must be read: %+v", page.Changes)
	}
	calls := 0
	for _, path := range mock.paths {
		if strings.HasSuffix(path, "/changelog") {
			calls++
		}
	}
	if calls != 3 {
		t.Fatalf("5 histories at page size 2 need 3 changelog requests, got %d", calls)
	}
}

func TestJiraChangesFailOnEndlessPageChain(t *testing.T) {
	mock := &pagedJira{pageSize: 1, loop: true, issues: []string{"APP-1", "APP-2"}, histories: map[string][]map[string]any{}}
	_, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}})
	if err == nil || !strings.Contains(err.Error(), "too many pages") {
		t.Fatalf("an endless chain must fail instead of truncating: %v", err)
	}
}

func TestJiraBearerAuthSendsTokenWithoutEmail(t *testing.T) {
	mock := &pagedJira{pageSize: 5, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
	adapter := mock.adapter(t, config.JiraAuthBearer)
	adapter.settings.Email = ""
	if _, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}}); err != nil {
		t.Fatal(err)
	}
	if len(mock.authz) == 0 {
		t.Fatal("no request seen")
	}
	for _, header := range mock.authz {
		if header != "Bearer secret-pat" {
			t.Fatalf("Authorization=%q, want Bearer token", header)
		}
	}
	basic := &pagedJira{pageSize: 5, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
	if _, err := basic.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(basic.authz[0], "Basic ") {
		t.Fatalf("default scheme must stay Basic: %q", basic.authz[0])
	}
}

func TestJiraChangesReadChangelogWhenServerOmitsTotal(t *testing.T) {
	// The embedded changelog has no total and holds a full page of histories
	// (100): it must be treated as possibly cut, and the full changelog read
	// even though /changelog has no total either.
	mock := &pagedJira{pageSize: 100, noTotal: true, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
	for i := 1; i <= 130; i++ {
		mock.histories["APP-1"] = append(mock.histories["APP-1"], pagedHistory(i))
	}
	mock.embedCut = 100
	page, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 130 || page.Changes[129].To != "S130" {
		t.Fatalf("all 130 histories must be read without totals, got %d", len(page.Changes))
	}
}

func TestJiraChangesKeepEmbeddedChangelogWhenShort(t *testing.T) {
	mock := &pagedJira{pageSize: 100, noTotal: true, issues: []string{"APP-1"}, histories: map[string][]map[string]any{"APP-1": {pagedHistory(1)}}}
	if _, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range mock.paths {
		if strings.HasSuffix(path, "/changelog") {
			t.Fatal("a short embedded changelog needs no extra request")
		}
	}
}

func TestJiraLegacySearchSurvivesIssuesShiftingBetweenPages(t *testing.T) {
	mock := &pagedJira{legacy: true, pageSize: 2, shiftOnUpdate: true, histories: map[string][]map[string]any{}}
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("APP-%d", i)
		mock.issues = append(mock.issues, key)
		mock.histories[key] = []map[string]any{pagedHistory(i)}
	}
	page, err := mock.adapter(t, "").Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: mock.issues})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 5 {
		t.Fatalf("a stable order must not skip issues between pages, got %d", len(page.Changes))
	}
}

func TestJiraChangesFallBackWhenChangelogEndpointIsMissing(t *testing.T) {
	mock := &pagedJira{pageSize: 2, embedCut: 2, noChangelog: true, issues: []string{"APP-1", "APP-2"}, histories: map[string][]map[string]any{}}
	for i := 1; i <= 4; i++ {
		mock.histories["APP-1"] = append(mock.histories["APP-1"], pagedHistory(i))
	}
	mock.histories["APP-2"] = []map[string]any{pagedHistory(10)}
	adapter := mock.adapter(t, "")
	page, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: mock.issues})
	if err != nil {
		t.Fatalf("a 404 from /changelog must not stop polling: %v", err)
	}
	if len(page.Changes) != 3 {
		t.Fatalf("embedded part of APP-1 (2) and APP-2 (1) expected, got %d", len(page.Changes))
	}
	if len(page.Warnings) != 1 || !strings.Contains(page.Warnings[0], "APP-1") || adapter.ChangelogFallbacks() != 1 {
		t.Fatalf("fallback must be reported once: warnings=%v counter=%d", page.Warnings, adapter.ChangelogFallbacks())
	}
}

func TestJiraBearerTokenWithWhitespaceIsRefusedWithoutEcho(t *testing.T) {
	for name, token := range map[string]string{"space": "abc def", "cr": "abc\rdef", "lf": "abc\ndef", "tab": "abc\tdef"} {
		t.Run(name, func(t *testing.T) {
			mock := &pagedJira{pageSize: 5, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
			adapter := mock.adapter(t, config.JiraAuthBearer)
			adapter.token = token
			_, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}})
			if err == nil || !strings.Contains(err.Error(), "whitespace or control") || strings.Contains(err.Error(), "def") {
				t.Fatalf("a token with inner whitespace must be refused without echo: %v", err)
			}
			if len(mock.authz) != 0 {
				t.Fatal("no request may carry a broken credential")
			}
		})
	}
	// Surrounding whitespace (a trailing newline) is dropped.
	mock := &pagedJira{pageSize: 5, issues: []string{"APP-1"}, histories: map[string][]map[string]any{}}
	adapter := mock.adapter(t, config.JiraAuthBearer)
	adapter.token = "  secret-pat\r\n"
	if _, err := adapter.Changes(context.Background(), ChangeQuery{Cursor: pagedCursor(), Keys: []string{"APP-1"}}); err != nil {
		t.Fatal(err)
	}
	if mock.authz[0] != "Bearer secret-pat" {
		t.Fatalf("Authorization=%q", mock.authz[0])
	}
}
