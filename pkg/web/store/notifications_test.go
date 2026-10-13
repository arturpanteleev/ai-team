package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
)

func openSharedStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	return first, second
}

func storedOutcome(t *testing.T, s *Store, channel string, eventID int64) string {
	t.Helper()
	var outcome string
	if err := s.db.QueryRow(`SELECT outcome FROM notification_deliveries WHERE channel = ? AND event_id = ?`, channel, eventID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	return outcome
}

// Two processes (web and run) open the same database file; the claim key is
// the channel/event pair, so exactly one of them owns each delivery.
func TestNotificationClaimIsSharedAcrossProcesses(t *testing.T) {
	web, run := openSharedStores(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	state, token, err := run.ClaimNotificationAt("ops-hook", 7, now)
	if err != nil || state != notifier.ClaimAcquired || token == "" {
		t.Fatalf("first claim=%v token=%q err=%v", state, token, err)
	}
	if state, other, _ := web.ClaimNotificationAt("ops-hook", 7, now.Add(time.Second)); state != notifier.ClaimBusy || other != "" {
		t.Fatalf("concurrent claim=%v token=%q, want busy without a token", state, other)
	}
	if err := run.RecordNotification("ops-hook", 7, token, notifier.DeliveryDelivered); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := web.ClaimNotificationAt("ops-hook", 7, now.Add(time.Hour)); state != notifier.ClaimHandled {
		t.Fatalf("claim after delivery=%v, want handled", state)
	}
	// The first final outcome wins: the same token cannot record twice.
	if err := web.RecordNotification("ops-hook", 7, token, notifier.DeliveryFailed); !errors.Is(err, notifier.ErrClaimLost) {
		t.Fatalf("second record err=%v, want ErrClaimLost", err)
	}
	if outcome := storedOutcome(t, web, "ops-hook", 7); outcome != notifier.DeliveryDelivered {
		t.Fatalf("outcome=%q", outcome)
	}
	// Other channels for the same event are independent.
	if state, _, err := web.ClaimNotificationAt("mail", 7, now); err != nil || state != notifier.ClaimAcquired {
		t.Fatalf("other channel claim=%v err=%v", state, err)
	}
}

func TestNotificationClaimExpiresAndCanBeReleased(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	state, first, err := s.ClaimNotificationAt("hook", 1, now)
	if err != nil || state != notifier.ClaimAcquired {
		t.Fatalf("claim=%v err=%v", state, err)
	}
	// A holder that died mid-delivery blocks others only until the lease ends.
	if state, _, _ := s.ClaimNotificationAt("hook", 1, now.Add(NotificationClaimLease-time.Second)); state != notifier.ClaimBusy {
		t.Fatalf("claim within lease=%v", state)
	}
	state, second, _ := s.ClaimNotificationAt("hook", 1, now.Add(NotificationClaimLease+time.Second))
	if state != notifier.ClaimAcquired || second == first {
		t.Fatalf("claim after lease=%v token reused=%v", state, second == first)
	}
	if err := s.ReleaseNotification("hook", 1, second); err != nil {
		t.Fatal(err)
	}
	state, third, _ := s.ClaimNotificationAt("hook", 1, now)
	if state != notifier.ClaimAcquired {
		t.Fatalf("claim after release=%v", state)
	}
	if err := s.RecordNotification("hook", 1, third, notifier.DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	// Releasing a finished delivery is a no-op.
	if err := s.ReleaseNotification("hook", 1, third); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := s.ClaimNotificationAt("hook", 1, now); state != notifier.ClaimHandled {
		t.Fatalf("claim after final outcome=%v", state)
	}
	if _, token, _ := s.ClaimNotificationAt("hook", 2, now); s.RecordNotification("hook", 2, token, "claimed:1") == nil {
		t.Fatal("a claim marker is not a final outcome")
	}
}

// A holder whose lease expired and was taken over must neither record over
// nor release the new holder's claim.
func TestNotificationRecordAndReleaseRequireTheOwnerToken(t *testing.T) {
	web, run := openSharedStores(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	_, stale, _ := run.ClaimNotificationAt("hook", 3, now)
	state, owner, err := web.ClaimNotificationAt("hook", 3, now.Add(NotificationClaimLease+time.Second))
	if err != nil || state != notifier.ClaimAcquired {
		t.Fatalf("takeover=%v err=%v", state, err)
	}
	if err := run.ReleaseNotification("hook", 3, stale); err != nil {
		t.Fatal(err)
	}
	if outcome := storedOutcome(t, web, "hook", 3); outcome != owner {
		t.Fatalf("stale release dropped the live claim: outcome=%q", outcome)
	}
	if err := run.RecordNotification("hook", 3, stale, notifier.DeliveryDelivered); !errors.Is(err, notifier.ErrClaimLost) {
		t.Fatalf("stale record err=%v, want ErrClaimLost", err)
	}
	if outcome := storedOutcome(t, web, "hook", 3); outcome != owner {
		t.Fatalf("stale record overwrote the live claim: outcome=%q", outcome)
	}
	if err := web.RecordNotification("hook", 3, owner, notifier.DeliveryDelivered); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordNotification("hook", 4, stale, notifier.DeliveryDelivered); !errors.Is(err, notifier.ErrClaimLost) {
		t.Fatalf("record without any claim err=%v, want ErrClaimLost", err)
	}
}

// Many goroutines on two handles of one database race for the same pair;
// exactly one of them may acquire it.
func TestNotificationClaimRaceAcquiresExactlyOnce(t *testing.T) {
	web, run := openSharedStores(t)
	const workers = 16
	start := make(chan struct{})
	results := make(chan notifier.ClaimState, workers)
	var wg sync.WaitGroup
	for index := range workers {
		handle := web
		if index%2 == 1 {
			handle = run
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			state, _, err := handle.ClaimNotification("race-hook", 11)
			if err != nil {
				// A locked database is a retryable busy outcome, not a win.
				state = notifier.ClaimBusy
			}
			results <- state
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	acquired := 0
	for state := range results {
		if state == notifier.ClaimAcquired {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired=%d, want exactly 1", acquired)
	}
}

func TestPruneNotificationDeliveriesKeepsLiveClaimsAndRecentRecords(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	old := now.Add(-NotificationDeliveryRetention - time.Hour)
	// Old final record, old live claim (its holder is still sending), recent
	// final record.
	if _, err := s.db.Exec(`INSERT INTO notification_deliveries(channel, event_id, outcome, recorded_at) VALUES
		('hook', 1, ?, ?), ('hook', 2, ?, ?), ('hook', 3, ?, ?)`,
		notifier.DeliveryDelivered, old, "claimed:1:abc", old, notifier.DeliveryFailed, now); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneNotificationDeliveries(now.Add(-NotificationDeliveryRetention))
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v, want 1", removed, err)
	}
	rows, err := s.db.Query(`SELECT event_id FROM notification_deliveries ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var kept []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, id)
	}
	if len(kept) != 2 || kept[0] != 2 || kept[1] != 3 {
		t.Fatalf("kept=%v, want [2 3]", kept)
	}
}
