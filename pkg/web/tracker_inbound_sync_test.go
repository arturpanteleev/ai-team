package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
)

func changesCalls(f *inboundFixture) int {
	count := 0
	for _, call := range f.fake.Calls() {
		if call.Op == "changes" {
			count++
		}
	}
	return count
}

// The cursor is written after the outcomes. When that write is refused the
// pass fails and the cursor stays; the retry reads the same change again and
// must treat it as a duplicate: the stage is not launched a second time.
func TestInboundCursorWriteRefusalRepeatsTheReadWithoutRelaunching(t *testing.T) {
	f := newInboundFixtureOn(t, true, defaultInboundRules)
	f.task(t, "inbound-cursor", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	before, err := f.srv.store.GetTrackerSyncState("fake")
	if err != nil || before == nil {
		t.Fatalf("state after start: %+v %v", before, err)
	}
	denyInbound(t, f, `CREATE TRIGGER deny_cursor BEFORE UPDATE ON tracker_sync_state
		WHEN NEW.last_success_at IS NOT OLD.last_success_at
		BEGIN SELECT RAISE(ABORT, 'sync state unavailable'); END`)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.syncTrackerInbound(context.Background(), "test"); err == nil {
		t.Fatal("a refused cursor write must fail the pass")
	}
	state, err := f.srv.store.GetTrackerSyncState("fake")
	if err != nil || state.Cursor != before.Cursor || state.ConsecutiveFailures != 1 || !strings.Contains(state.LastError, "sync state unavailable") {
		t.Fatalf("cursor must stay and the failure must be visible: %+v err=%v", state, err)
	}
	if len(f.controller.agentCalls) != 1 {
		t.Fatalf("the change was decided once before the write failed: %v", f.controller.agentCalls)
	}
	// Storage recovers (the trigger is dropped by a second connection).
	denyInbound(t, f, `DROP TRIGGER deny_cursor`)
	retry := f.sync(t)
	if retry.Changes != 1 || retry.Duplicates != 1 || retry.Applied != 0 || len(f.controller.agentCalls) != 1 {
		t.Fatalf("the replayed change must be a duplicate, not a second launch: %+v calls=%v", retry, f.controller.agentCalls)
	}
	state, _ = f.srv.store.GetTrackerSyncState("fake")
	if state.Cursor == before.Cursor || state.ConsecutiveFailures != 0 || state.LastError != "" {
		t.Fatalf("cursor must advance after recovery: %+v", state)
	}
	if got := f.requests(t, ""); len(got) != 1 {
		t.Fatalf("exactly one request expected: %+v", got)
	}
}

// One pass over mixed changes: every counter is exact, a replay of the same
// changes is all duplicates, and the persisted last_changes matches.
func TestInboundSyncCountersAreExactAndReplayIsAllDuplicates(t *testing.T) {
	rules := append([]tracker.InboundRule{
		{Status: "Blocked", Action: tracker.ActionAdvance},
		{Status: "Blocked", Action: tracker.ActionReturn, ToStage: "implementation"},
	}, defaultInboundRules...)
	f := newInboundFixture(t, rules, confirmApproval("inbound-count-b", "ap-count", "code_review"))
	f.task(t, "inbound-count-a", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.task(t, "inbound-count-b", "APP-2", lifecycle.PhaseWaiting, "code_review", "ap-count")
	f.start(t)

	for _, step := range []struct{ key, status string }{
		{"APP-1", "In Progress"}, // applied: the ready stage continues
		{"APP-2", "Done"},        // pending: a confirmation is not a tracker decision
		{"APP-2", "Blocked"},     // conflict: two different requests match
		{"APP-2", "In Review"},   // ignored: not mapped
	} {
		if err := f.fake.ExternalTransition(step.key, step.status, "pat"); err != nil {
			t.Fatal(err)
		}
	}
	result := f.sync(t)
	if result.Changes != 4 || result.Applied != 1 || result.Pending != 1 || result.Conflicts != 1 || result.Ignored != 1 || result.Duplicates != 0 {
		t.Fatalf("counters: %+v", result)
	}
	state, err := f.srv.store.GetTrackerSyncState("fake")
	if err != nil || state.LastChanges != 4 || state.Cursor != result.Cursor {
		t.Fatalf("persisted state: %+v err=%v", state, err)
	}
	if err := f.srv.store.SaveTrackerSyncCursor("fake", "0", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	replay := f.sync(t)
	if replay.Changes != 4 || replay.Duplicates != 4 || replay.Applied+replay.Pending+replay.Conflicts+replay.Ignored != 0 || len(f.controller.agentCalls) != 1 {
		t.Fatalf("replay counters: %+v calls=%v", replay, f.controller.agentCalls)
	}
	// An empty pass reports zero everywhere and keeps the cursor.
	empty := f.sync(t)
	if empty.Changes != 0 || empty.Duplicates != 0 || empty.Cursor != replay.Cursor {
		t.Fatalf("empty pass: %+v", empty)
	}
}

// More changes than one batch are read in several pages: all are counted and
// decided once, and the cursor ends at the last one.
func TestInboundSyncReadsEveryBatchOfChanges(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-batches", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	total := 2*trackerInboundBatch + 7
	for i := 0; i < total; i++ {
		status := "In Review"
		if i%2 == 1 {
			status = "To Do"
		}
		if err := f.fake.ExternalTransition("APP-1", status, fmt.Sprintf("user-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	result := f.sync(t)
	if result.Changes != total || result.Ignored != total || result.Duplicates != 0 {
		t.Fatalf("counters over %d changes: %+v", total, result)
	}
	state, _ := f.srv.store.GetTrackerSyncState("fake")
	if state.LastChanges != total || state.Cursor != result.Cursor || state.Cursor != fmt.Sprint(total) {
		t.Fatalf("state: %+v result cursor %q", state, result.Cursor)
	}
	if again := f.sync(t); again.Changes != 0 {
		t.Fatalf("nothing may be read twice: %+v", again)
	}
}

// A failed manual pass is not reused: the operator's retry reads the tracker
// again. A poll never reuses a manual pass and never refreshes the window.
func TestInboundCoalescingIgnoresFailuresAndPolls(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-gap", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	now := time.Now().UTC()
	f.srv.now = func() time.Time { return now }
	ctx := context.Background()

	f.fake.FailNext("changes", errors.New("tracker timeout"), 1)
	if _, err := f.srv.syncTrackerInbound(ctx, "manual"); err == nil {
		t.Fatal("the first manual pass must fail")
	}
	ok, err := f.srv.syncTrackerInbound(ctx, "manual")
	if err != nil || ok.Coalesced {
		t.Fatalf("a retry after a failure must read the tracker: %+v %v", ok, err)
	}
	reads := changesCalls(f)
	poll, err := f.srv.syncTrackerInbound(ctx, "poll")
	if err != nil || poll.Coalesced || changesCalls(f) != reads+1 {
		t.Fatalf("a poll always reads the tracker: %+v %v (reads %d -> %d)", poll, err, reads, changesCalls(f))
	}
	// The poll did not refresh the window of the manual pass.
	now = now.Add(trackerManualMinGap + time.Millisecond)
	later, err := f.srv.syncTrackerInbound(ctx, "manual")
	if err != nil || later.Coalesced {
		t.Fatalf("after the window a manual pass reads again: %+v %v", later, err)
	}
	// A failure after a coalesced window closes it: no stale success is reused.
	f.fake.FailNext("changes", errors.New("tracker timeout"), 1)
	now = now.Add(trackerManualMinGap + time.Millisecond)
	if _, err := f.srv.syncTrackerInbound(ctx, "manual"); err == nil {
		t.Fatal("pass must fail")
	}
	if again, err := f.srv.syncTrackerInbound(ctx, "manual"); err != nil || again.Coalesced {
		t.Fatalf("no coalescing right after a failure: %+v %v", again, err)
	}
}

// A burst of concurrent "sync now" calls is one tracker read.
func TestInboundConcurrentManualSyncReadsTheTrackerOnce(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-burst-concurrent", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	now := time.Now().UTC()
	f.srv.now = func() time.Time { return now }
	baseline := changesCalls(f)
	var wg sync.WaitGroup
	results := make([]trackerSyncResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.srv.syncTrackerInbound(context.Background(), "manual")
			if err != nil {
				t.Error(err)
			}
			results[i] = result
		}()
	}
	wg.Wait()
	coalesced := 0
	for _, result := range results {
		if result.Coalesced {
			coalesced++
		}
	}
	if coalesced != len(results)-1 || changesCalls(f)-baseline != 1 {
		t.Fatalf("coalesced=%d of %d, tracker reads=%d", coalesced, len(results), changesCalls(f)-baseline)
	}
}
