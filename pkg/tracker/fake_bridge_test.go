package tracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

func openBridgedFake(t *testing.T) (*Fake, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.TrackerConfig{Fake: &config.FakeTrackerConfig{Issues: []config.FakeIssueConfig{{Key: "APP-1"}}}}
	adapter, err := openFake(cfg, Deps{Getenv: func(name string) string {
		if name == FakeDirEnv {
			return dir
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.(*Fake), dir
}

func appendInbox(t *testing.T, dir, text string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, "inbox.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestFakeInboxAppliesExternalChangesOnce(t *testing.T) {
	fake, dir := openBridgedFake(t)
	ctx := context.Background()
	if page, err := fake.Changes(ctx, ChangeQuery{}); err != nil || page.Cursor != "0" {
		t.Fatalf("first read records the tail: %+v %v", page, err)
	}
	appendInbox(t, dir, "not json\n"+
		`{"key":"NOPE-1","status":"Done","author":"x"}`+"\n"+
		`{"key":"APP-1","status":"nonexistent","author":"x"}`+"\n"+
		`{"key":"APP-1","status":"done","author":"Ivan"}`+"\n")
	page, err := fake.Changes(ctx, ChangeQuery{Cursor: "0"})
	if err != nil || len(page.Changes) != 1 {
		t.Fatalf("exactly the valid line must become a change: %+v %v", page, err)
	}
	change := page.Changes[0]
	if change.IssueKey != "APP-1" || change.To != "Done" || change.Author != "Ivan" || change.Origin != OriginExternal {
		t.Fatalf("change: %+v", change)
	}
	if again, err := fake.Changes(ctx, ChangeQuery{Cursor: page.Cursor}); err != nil || len(again.Changes) != 0 {
		t.Fatalf("a replayed read must not apply the inbox twice: %+v %v", again, err)
	}
	if status, _ := fake.Status("APP-1"); status != "Done" {
		t.Fatalf("status = %q", status)
	}
}

func TestFakeInboxWaitsForCompleteLine(t *testing.T) {
	fake, dir := openBridgedFake(t)
	ctx := context.Background()
	appendInbox(t, dir, `{"key":"APP-1","status":"Done"`)
	if page, err := fake.Changes(ctx, ChangeQuery{Cursor: "0"}); err != nil || len(page.Changes) != 0 {
		t.Fatalf("a half-written line must wait: %+v %v", page, err)
	}
	appendInbox(t, dir, `,"author":"Olga"}`+"\n")
	if page, err := fake.Changes(ctx, ChangeQuery{Cursor: "0"}); err != nil || len(page.Changes) != 1 {
		t.Fatalf("the completed line must be applied: %+v %v", page, err)
	}
}

func TestFakeOutboxRecordsAcceptedWrites(t *testing.T) {
	fake, dir := openBridgedFake(t)
	ctx := context.Background()
	if moved, err := fake.Transition(ctx, "APP-1", "In Progress"); err != nil || !moved {
		t.Fatalf("transition: %v %v", moved, err)
	}
	if added, err := fake.AddComment(ctx, "APP-1", Comment{Key: "k1", Lines: []string{"этап пройден"}}); err != nil || !added {
		t.Fatalf("comment: %v %v", added, err)
	}
	// A duplicate comment is not an accepted write.
	if added, _ := fake.AddComment(ctx, "APP-1", Comment{Key: "k1", Lines: []string{"этап пройден"}}); added {
		t.Fatal("duplicate comment accepted")
	}
	data, err := os.ReadFile(filepath.Join(dir, "outbox.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"op":"transition"`) || !strings.Contains(lines[0], `"status":"In Progress"`) ||
		!strings.Contains(lines[1], `"op":"comment"`) || !strings.Contains(lines[1], "этап пройден") {
		t.Fatalf("outbox: %q", data)
	}
}

func TestFakeBridgeRefusesSymlinks(t *testing.T) {
	fake, dir := openBridgedFake(t)
	outside := t.TempDir()
	secretInbox := filepath.Join(outside, "elsewhere-inbox.jsonl")
	if err := os.WriteFile(secretInbox, []byte(`{"key":"APP-1","status":"Done","author":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretInbox, filepath.Join(dir, "inbox.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target := filepath.Join(outside, "elsewhere-outbox.jsonl")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "outbox.jsonl")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if page, err := fake.Changes(ctx, ChangeQuery{Cursor: "0"}); err != nil || len(page.Changes) != 0 {
		t.Fatalf("an inbox that is a symlink must not be read: %+v %v", page, err)
	}
	if _, err := fake.Transition(ctx, "APP-1", "In Progress"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); len(data) != 0 {
		t.Fatalf("an outbox that is a symlink must not be written through: %q", data)
	}
}

func TestFakeInboxRestartsAfterTruncation(t *testing.T) {
	fake, dir := openBridgedFake(t)
	ctx := context.Background()
	appendInbox(t, dir, `{"key":"APP-1","status":"In Progress","author":"Ivan"}`+"\n")
	if page, err := fake.Changes(ctx, ChangeQuery{Cursor: "0"}); err != nil || len(page.Changes) != 1 {
		t.Fatalf("first change: %+v %v", page, err)
	}
	// The file is emptied and a shorter line written: the offset must not skip it.
	if err := os.WriteFile(filepath.Join(dir, "inbox.jsonl"), []byte(`{"key":"APP-1","status":"Done"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := fake.Changes(ctx, ChangeQuery{Cursor: "1"})
	if err != nil || len(page.Changes) != 1 || page.Changes[0].To != "Done" {
		t.Fatalf("a truncated inbox must be read from the start: %+v %v", page, err)
	}
}

func TestFakeWithoutDirDoesNotTouchTheFilesystem(t *testing.T) {
	adapter, err := openFake(&config.TrackerConfig{Fake: &config.FakeTrackerConfig{Issues: []config.FakeIssueConfig{{Key: "APP-1"}}}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	fake := adapter.(*Fake)
	if _, err := fake.Transition(context.Background(), "APP-1", "Done"); err != nil || fake.outboxPath != "" || fake.inboxPath != "" {
		t.Fatalf("no bridge expected: %v %q %q", err, fake.inboxPath, fake.outboxPath)
	}
}
