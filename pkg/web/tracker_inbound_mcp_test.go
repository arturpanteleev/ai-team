package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/tracker"
)

func TestMCPReadsTrackerSyncStateAndPendingRequests(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	env := newMCPEnv(t, WithTracker(fake, trackerTestMaps, "https://ai-team.example"),
		WithTrackerInbound([]tracker.InboundRule{{Status: "Done", Action: tracker.ActionAdvance}}, time.Hour))
	run, err := env.srv.store.GetPipelineRunByRunID(mcpTestRun)
	if err != nil || run == nil {
		t.Fatalf("seeded run: %v", err)
	}
	if _, err := env.srv.store.SetTrackerLink(run.ID, "fake", "APP-1", "owner@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.syncTrackerInbound(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := fake.ExternalTransition("APP-1", "Done", "ivan"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.syncTrackerInbound(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	token, _ := env.issueToken(t, "owner@example.com", `{"label":"tracker"}`)
	session := env.mustConnect(t, token)
	for _, tc := range []struct {
		tool string
		args map[string]any
		rest string
	}{
		{"get_tracker_status", nil, "/api/tracker"},
		{"get_tracker_sync", nil, "/api/tracker/sync"},
		{"list_tracker_inbound", map[string]any{"state": "open"}, "/api/tracker/inbound?state=open"},
		{"get_task_tracker", map[string]any{"run_id": mcpTestRun}, "/api/runs/" + mcpTestRun + "/tracker"},
	} {
		text, isError := callTool(t, session, tc.tool, tc.args)
		code, body := env.rest(t, "owner@example.com", tc.rest)
		if isError || code != http.StatusOK {
			t.Fatalf("%s: error=%v rest=%d %s", tc.tool, isError, code, text)
		}
		if tc.tool == "list_tracker_inbound" {
			// Paginated locally: check the request itself.
			if !strings.Contains(text, `"state":"pending"`) || !strings.Contains(text, `"to_status":"Done"`) {
				t.Fatalf("pending request missing from MCP: %s", text)
			}
			continue
		}
		sameJSON(t, tc.tool, []byte(text), body)
	}
}
