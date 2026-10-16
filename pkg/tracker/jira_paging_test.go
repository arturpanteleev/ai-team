package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		parts := strings.Split(r.URL.Path, "/")
		all := p.histories[parts[len(parts)-2]]
		start, _ := strconv.Atoi(q.Get("startAt"))
		end := min(start+p.pageSize, len(all))
		_ = json.NewEncoder(w).Encode(map[string]any{"startAt": start, "total": len(all), "isLast": end >= len(all), "values": all[start:end]})
	case strings.HasSuffix(r.URL.Path, "/search/jql") || strings.HasSuffix(r.URL.Path, "/search"):
		if p.legacy == strings.HasSuffix(r.URL.Path, "/search/jql") {
			http.NotFound(w, r)
			return
		}
		start := 0
		if p.legacy {
			start, _ = strconv.Atoi(q.Get("startAt"))
		} else if tok := q.Get("nextPageToken"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+p.pageSize, len(p.issues))
		items := []map[string]any{}
		for _, key := range p.issues[start:end] {
			hs := p.histories[key]
			total := len(hs)
			if p.embedCut > 0 && len(hs) > p.embedCut {
				hs = hs[:p.embedCut]
			}
			items = append(items, map[string]any{"key": key, "changelog": map[string]any{"total": total, "histories": hs}})
		}
		body := map[string]any{"issues": items}
		switch {
		case p.legacy:
			body["total"] = len(p.issues)
			body["startAt"] = start
		case end < len(p.issues) || p.loop:
			body["nextPageToken"] = strconv.Itoa(end % max(len(p.issues), 1))
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
