package store

import (
	"strings"
	"testing"
	"time"
)

func TestAppendAuditRecordsAnMCPActionInTheTeamAudit(t *testing.T) {
	s := newTestStore(t)
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := s.AppendAudit("owner@example.com", "mcp_action", "", map[string]any{"tool": "create_project", "channel": "mcp"}, at); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListTeamAudit(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "mcp_action" || entries[0].ActorID != "owner@example.com" ||
		!strings.Contains(string(entries[0].Details), `"channel":"mcp"`) || !entries[0].CreatedAt.Equal(at) {
		t.Fatalf("audit = %+v", entries)
	}
	if err = s.AppendAudit("owner@example.com", "x", "", make(chan int), at); err == nil {
		t.Fatal("details that cannot be encoded must be refused")
	}
}
