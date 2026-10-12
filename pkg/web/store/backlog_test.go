package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
)

func newBacklogStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBacklogItemsStartAsDraftAndAreNeverPicked(t *testing.T) {
	s := newBacklogStore(t)
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	item, err := s.CreateBacklogItem("Идея", "Описание", "idea", "po-1", at)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != autopickup.StatusDraft || item.CreatedBy != "po-1" || item.ReadyAt != nil {
		t.Fatalf("new item must be a draft: %+v", item)
	}
	if _, found, err := s.NextReadyBacklogItem(); err != nil || found {
		t.Fatalf("draft must not be in the pickup queue: found=%t err=%v", found, err)
	}
}

// Только точное значение ready делает элемент доступным: неизвестный признак
// (ручная правка базы, будущая версия) элемент не выдаёт.
func TestNextReadyIgnoresUnknownMarkers(t *testing.T) {
	for _, status := range []string{"READY", "ready ", "auto", "approved", "", "starting", "started", "failed", "discarded"} {
		t.Run(status, func(t *testing.T) {
			s := newBacklogStore(t)
			item, err := s.CreateBacklogItem("Идея", "", "idea", "po-1", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE backlog_items SET status = ?, ready_at = ? WHERE id = ?`, status, time.Now().UTC(), item.ID); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.NextReadyBacklogItem(); err != nil || found {
				t.Fatalf("status %q must not be picked: found=%t err=%v", status, found, err)
			}
			if claimed, err := s.ClaimBacklogItem(item.ID, time.Now()); err != nil || claimed {
				t.Fatalf("status %q must not be claimable: claimed=%t err=%v", status, claimed, err)
			}
		})
	}
}

func TestReadyQueueOrderAndClaimLifecycle(t *testing.T) {
	s := newBacklogStore(t)
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	first, _ := s.CreateBacklogItem("Первая", "", "first", "po", base)
	second, _ := s.CreateBacklogItem("Вторая", "", "second", "po", base)
	// Порядок очереди — момент пометки ready, а не создания.
	if _, err := s.SetBacklogItemStatus(second.ID, autopickup.StatusReady, "po-2", base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBacklogItemStatus(first.ID, autopickup.StatusReady, "po-1", base.Add(2*time.Minute+500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	next, found, err := s.NextReadyBacklogItem()
	if err != nil || !found || next.ID != second.ID || next.ReadyBy != "po-2" || next.ReadyAt == nil {
		t.Fatalf("queue head = %+v found=%t err=%v, want item %d", next, found, err, second.ID)
	}
	claimed, err := s.ClaimBacklogItem(second.ID, base.Add(3*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	if again, err := s.ClaimBacklogItem(second.ID, base.Add(3*time.Minute)); err != nil || again {
		t.Fatalf("second claim must lose: %t %v", again, err)
	}
	if _, err := s.SetBacklogItemStatus(second.ID, autopickup.StatusDraft, "po", base); !errors.Is(err, ErrBacklogTransition) {
		t.Fatalf("human cannot move a claimed item: %v", err)
	}
	if err := s.BindBacklogRun(second.ID, "run-1", base); err != nil {
		t.Fatal(err)
	}
	if err := s.BindBacklogRun(second.ID, "run-2", base); err == nil {
		t.Fatal("claimed item must bind exactly one run")
	}
	starting, err := s.StartingBacklogItems()
	if err != nil || len(starting) != 1 || starting[0].RunID != "run-1" {
		t.Fatalf("starting items = %+v err=%v", starting, err)
	}
	if err := s.FinishBacklogClaim(second.ID, "ready", "", base); err == nil {
		t.Fatal("claim can finish only as started or failed")
	}
	if err := s.FinishBacklogClaim(second.ID, autopickup.StatusStarted, "", base); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBacklogClaim(second.ID, autopickup.StatusFailed, "late", base); err == nil {
		t.Fatal("finished claim must not be finished again")
	}
	next, found, err = s.NextReadyBacklogItem()
	if err != nil || !found || next.ID != first.ID {
		t.Fatalf("next queue head = %+v found=%t err=%v", next, found, err)
	}
	items, err := s.ListBacklogItems()
	if err != nil || len(items) != 2 || items[1].Status != autopickup.StatusStarted || items[1].RunID != "run-1" {
		t.Fatalf("list = %+v err=%v", items, err)
	}
}

func TestFailedItemReturnsToQueueOnlyByHuman(t *testing.T) {
	s := newBacklogStore(t)
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	item, _ := s.CreateBacklogItem("Идея", "", "idea", "po", at)
	if _, err := s.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po", at); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimBacklogItem(item.ID, at); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err := s.BindBacklogRun(item.ID, "run-lost", at); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBacklogClaim(item.ID, autopickup.StatusFailed, "boom", at); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.NextReadyBacklogItem(); found {
		t.Fatal("failed item must not be retried automatically")
	}
	again, err := s.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po-2", at.Add(time.Hour))
	if err != nil || again.Status != autopickup.StatusReady || again.RunID != "" || again.Error != "" || again.ReadyBy != "po-2" {
		t.Fatalf("human re-ready = %+v err=%v", again, err)
	}
	if _, err := s.SetBacklogItemStatus(item.ID, autopickup.StatusDiscarded, "po", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po", at); !errors.Is(err, ErrBacklogTransition) {
		t.Fatalf("discarded item is final: %v", err)
	}
	if _, err := s.SetBacklogItemStatus(9999, autopickup.StatusReady, "po", at); err == nil {
		t.Fatal("missing item must be an error")
	}
}

func TestBacklogSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateBacklogItem("Идея", "", "idea", "po", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	next, found, err := reopened.NextReadyBacklogItem()
	if err != nil || !found || next.ID != item.ID {
		t.Fatalf("ready item lost after reopen: %+v found=%t err=%v", next, found, err)
	}
}

func TestValidBacklogText(t *testing.T) {
	if ValidBacklogText("  ", 10) || ValidBacklogText("слишком длинно", 3) || !ValidBacklogText("ok", 10) {
		t.Fatal("backlog text validation")
	}
}
