package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestTrackerRetryDoesNotLoseTransitionWhenNewerEventDeliveredOnlyAComment(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "lost-transition-run", nil)
	linkTask(t, srv, run, "APP-1")

	fake.FailNext("transition", errors.New("tracker is down"), -1)
	trackerEvent(t, srv, run, "attempt_finished", "implementation", "att-1", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "failed transition delivery", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1
	})
	fake.ClearFailures()
	// A stage without on_pass: only a comment is delivered, no transition.
	trackerEvent(t, srv, run, "attempt_finished", "design", "att-2", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "comment-only delivery", func() bool {
		delivered, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryDelivered, 10)
		return len(delivered) == 1
	})
	delivered, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryDelivered, 10)
	if delivered[0].TransitionTo != "" {
		t.Fatalf("a comment-only delivery must not claim a transition: %+v", delivered[0])
	}
	failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
	retry := fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", run.RunID, failed[0].EventID)
	r := projectServe(t, srv, http.MethodPost, retry, "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"delivered"`) || !strings.Contains(r.Body.String(), `"transition_to":"In Review"`) {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	if s, _ := fake.Status("APP-1"); s != "In Review" {
		t.Fatalf("the transition was lost: status=%s", s)
	}
}

func TestTrackerSupersededStaleTransitionIsExplicitAndKeepsTheOriginalError(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "superseded-note-run", nil)
	linkTask(t, srv, run, "APP-1")

	fake.FailNext("transition", errors.New("tracker is down"), -1)
	trackerEvent(t, srv, run, "attempt_finished", "implementation", "att-1", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "failed delivery", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1
	})
	fake.ClearFailures()
	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-2", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "newer transition", func() bool {
		delivered, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryDelivered, 10)
		return len(delivered) == 1 && delivered[0].TransitionTo == "Done"
	})
	failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
	retry := fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", run.RunID, failed[0].EventID)
	r := projectServe(t, srv, http.MethodPost, retry, "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"superseded"`) {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	row, err := srv.store.GetTrackerDelivery(failed[0].EventID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.TrackerDeliverySuperseded || row.TransitionTo != "" || row.Note == "" || !strings.Contains(row.LastError, "tracker is down") {
		t.Fatalf("superseded row must say the transition was not performed and keep the error: %+v", row)
	}
	if s, _ := fake.Status("APP-1"); s != "Done" {
		t.Fatalf("status=%s, want Done", s)
	}
}

func TestIntegrationConsumerPausesAfterFailureAndResetsAfterSuccess(t *testing.T) {
	fake := tracker.NewFake(nil)
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "backoff-run", nil)
	var mu sync.Mutex
	var calls int
	succeed := false
	handled := map[int64]int{}
	handler := func(_ context.Context, event store.Event) bool {
		mu.Lock()
		defer mu.Unlock()
		if event.RunID != run.RunID || event.Type != "attempt_started" {
			return true
		}
		calls++
		if !succeed {
			return false
		}
		handled[event.ID]++
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !srv.startIntegrationConsumer(ctx, "backoff-test", 0, handler) {
		t.Fatal("consumer did not start")
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }
	trackerEvent(t, srv, run, "attempt_started", "implementation", "att-1", `{}`)
	eventually(t, "first failing call", func() bool { return count() == 1 })
	time.Sleep(350 * time.Millisecond) // inside the 500ms pause, past a 250ms tick
	if count() != 1 {
		t.Fatalf("the handler was called during the pause: %d calls", count())
	}
	eventually(t, "retry after the pause", func() bool { return count() >= 2 })
	mu.Lock()
	succeed = true
	mu.Unlock()
	eventually(t, "success after the second pause", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	})
	// After a successful drain the pause is reset: a new event is handled on
	// the next tick, not after a doubled pause.
	trackerEvent(t, srv, run, "attempt_started", "qa", "att-2", `{}`)
	start := time.Now()
	eventually(t, "prompt handling after reset", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 2
	})
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("pause was not reset after success: %v", elapsed)
	}
}
