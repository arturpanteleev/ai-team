package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
)

// Two processes (web and run) open the same database file; the claim key is
// the channel/event pair, so exactly one of them owns each delivery.
func TestNotificationClaimIsSharedAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	web, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = web.Close() }()
	run, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	claim := func(s *Store, at time.Time) notifier.ClaimState {
		t.Helper()
		state, err := s.ClaimNotificationAt("ops-hook", 7, at)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	if got := claim(run, now); got != notifier.ClaimAcquired {
		t.Fatalf("first claim=%v", got)
	}
	if got := claim(web, now.Add(time.Second)); got != notifier.ClaimBusy {
		t.Fatalf("concurrent claim=%v, want busy", got)
	}
	if err := run.RecordNotification("ops-hook", 7, notifier.DeliveryDelivered); err != nil {
		t.Fatal(err)
	}
	if got := claim(web, now.Add(time.Hour)); got != notifier.ClaimHandled {
		t.Fatalf("claim after delivery=%v, want handled", got)
	}
	// The first final outcome wins.
	if err := web.RecordNotification("ops-hook", 7, notifier.DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := web.db.QueryRow(`SELECT outcome FROM notification_deliveries WHERE channel = 'ops-hook' AND event_id = 7`).Scan(&outcome); err != nil || outcome != notifier.DeliveryDelivered {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	// Other channels for the same event are independent.
	if state, err := web.ClaimNotificationAt("mail", 7, now); err != nil || state != notifier.ClaimAcquired {
		t.Fatalf("other channel claim=%v err=%v", state, err)
	}
}

func TestNotificationClaimExpiresAndCanBeReleased(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if state, err := s.ClaimNotificationAt("hook", 1, now); err != nil || state != notifier.ClaimAcquired {
		t.Fatalf("claim=%v err=%v", state, err)
	}
	// A holder that died mid-delivery blocks others only until the lease ends.
	if state, _ := s.ClaimNotificationAt("hook", 1, now.Add(NotificationClaimLease-time.Second)); state != notifier.ClaimBusy {
		t.Fatalf("claim within lease=%v", state)
	}
	if state, _ := s.ClaimNotificationAt("hook", 1, now.Add(NotificationClaimLease+time.Second)); state != notifier.ClaimAcquired {
		t.Fatalf("claim after lease=%v", state)
	}
	if err := s.ReleaseNotification("hook", 1); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.ClaimNotificationAt("hook", 1, now); state != notifier.ClaimAcquired {
		t.Fatalf("claim after release=%v", state)
	}
	if err := s.RecordNotification("hook", 1, notifier.DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	// Releasing a finished delivery is a no-op.
	if err := s.ReleaseNotification("hook", 1); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.ClaimNotificationAt("hook", 1, now); state != notifier.ClaimHandled {
		t.Fatalf("claim after final outcome=%v", state)
	}
	if err := s.RecordNotification("hook", 2, "claimed:1"); err == nil {
		t.Fatal("a claim marker is not a final outcome")
	}
}
