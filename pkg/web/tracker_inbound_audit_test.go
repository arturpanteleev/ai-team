package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func inboundEvents(t *testing.T, f *inboundFixture, runID, eventType string) []map[string]any {
	t.Helper()
	events, err := f.srv.allRunEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		found = append(found, data)
	}
	return found
}

func TestInboundLaunchIsAuditedUnderTheAdapterAndGrantsNoAuthorship(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-audit", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "Mallory <admin>"); err != nil {
		t.Fatal(err)
	}
	if result := f.sync(t); result.Applied != 1 {
		t.Fatalf("result: %+v", result)
	}
	launches := inboundEvents(t, f, "inbound-audit", trackerInboundLaunchEvent)
	if len(launches) != 1 || launches[0]["actor_id"] != "tracker:fake" || launches[0]["adapter"] != "fake" ||
		launches[0]["change_id"] == "" || launches[0]["stage_id"] != "implementation" || launches[0]["untrusted_author"] != "Mallory <admin>" {
		t.Fatalf("launch audit: %+v", launches)
	}
	// The display name is never an identity: it is not in the actor handed to the controller.
	for _, call := range f.controller.agentCalls {
		if strings.Contains(call, "Mallory") {
			t.Fatalf("author name leaked into the actor: %v", f.controller.agentCalls)
		}
	}
	// A tracker launch is neither human authorship nor a human launch event.
	if stageAuthorEventTypes[trackerInboundLaunchEvent] || len(inboundEvents(t, f, "inbound-audit", stageAgentRequestedEvent)) != 0 {
		t.Fatal("a tracker launch must not count as stage authorship")
	}
}

func TestInboundReplayAfterCrashDoesNotLaunchTheNextStage(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	run := f.task(t, "inbound-crash", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "ivan"); err != nil {
		t.Fatal(err)
	}
	// The earlier attempt recorded the stage it launched and died before the
	// outcome was stored: the record is still "received".
	changes, err := f.fake.Changes(context.Background(), tracker.ChangeQuery{Cursor: "0", Keys: []string{"APP-1"}})
	if err != nil || len(changes.Changes) != 1 {
		t.Fatalf("changes: %+v %v", changes, err)
	}
	change := changes.Changes[0]
	record, _, err := f.srv.store.BeginTrackerInbound(store.TrackerInbound{
		Tracker: "fake", ChangeID: change.ID, TaskID: run.ID, IssueKey: "APP-1", ToStatus: "In Progress",
		Origin: change.Origin, Author: "ivan", ChangedAt: change.At,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record.Stage, record.Action = "implementation", tracker.ActionAdvance
	if _, err := f.srv.store.ResolveTrackerInbound(record, time.Now()); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(f.target)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := stateStore.Load(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Phase, next.NextStage = lifecycle.PhaseRunning, "qa"
	if err := stateStore.Save(previous, next); err != nil {
		t.Fatal(err)
	}
	// Re-read the change from before the tail.
	if err := f.srv.store.SaveTrackerSyncCursor("fake", "0", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if len(f.controller.agentCalls) != 0 {
		t.Fatalf("a stale request launched the next stage: %v", f.controller.agentCalls)
	}
	superseded := f.requests(t, "superseded")
	if len(superseded) != 1 || !strings.Contains(superseded[0].Reason, "no longer ready") {
		t.Fatalf("superseded: %+v", superseded)
	}
}

func TestInboundChangeMadeBeforeTheStageWasEnteredIsStale(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-stale", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	f.fake.SetChangeClock(func() time.Time { return time.Now().Add(-time.Hour) })
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "olga"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if len(f.controller.agentCalls) != 0 {
		t.Fatalf("an old status launched the stage: %v", f.controller.agentCalls)
	}
	superseded := f.requests(t, "superseded")
	if len(superseded) != 1 || !strings.Contains(superseded[0].Reason, "before the task entered stage") {
		t.Fatalf("superseded: %+v", superseded)
	}
}

func TestInboundUntrustedFieldsAreCleanedAndBounded(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-clean", "APP-1", lifecycle.PhaseWaiting, "code_review", "")
	f.start(t)
	author := "Eve\n\x1b[31m‮root" + strings.Repeat("x", 1000)
	if err := f.fake.ExternalTransition("APP-1", "Weird\tstatus\r\n"+strings.Repeat("y", 1000), author); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	all := f.requests(t, "")
	if len(all) != 1 {
		t.Fatalf("requests: %+v", all)
	}
	for name, value := range map[string]string{"author": all[0].Author, "to": all[0].ToStatus, "from": all[0].FromStatus, "id": all[0].ChangeID, "reason": all[0].Reason} {
		if len(value) > 512 || strings.ContainsAny(value, "\n\r\t\x1b‮") {
			t.Fatalf("%s is not clean: %q", name, value)
		}
	}
	if len(all[0].Author) > 256 || len(all[0].ToStatus) > 256 || !strings.HasPrefix(all[0].Author, "Eve") {
		t.Fatalf("author=%q to=%d", all[0].Author, len(all[0].ToStatus))
	}
}

func TestInboundSyncNowIsCoalescedWithinTheMinimumInterval(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-burst", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	now := time.Now().UTC()
	f.srv.now = func() time.Time { return now }
	first, err := f.srv.syncTrackerInbound(context.Background(), "manual")
	if err != nil || first.Coalesced {
		t.Fatalf("first: %+v %v", first, err)
	}
	f.fake.SetChangeClock(func() time.Time { return now.Add(time.Second) })
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "pat"); err != nil {
		t.Fatal(err)
	}
	second, err := f.srv.syncTrackerInbound(context.Background(), "manual")
	if err != nil || !second.Coalesced || second.Changes != 0 || second.Applied != 0 || second.Pending != 0 ||
		second.Conflicts != 0 || second.Ignored != 0 || second.Duplicates != 0 {
		t.Fatalf("second inside the window must reuse the last pass: %+v %v", second, err)
	}
	// A clock stepped back must not freeze the window: the pass is read again.
	now = now.Add(-time.Hour)
	back, err := f.srv.syncTrackerInbound(context.Background(), "manual")
	if err != nil || back.Coalesced || back.Changes != 1 {
		t.Fatalf("a clock set back must not coalesce: %+v %v", back, err)
	}
	now = now.Add(time.Hour)
	// A later manual call reads the tracker again.
	now = now.Add(trackerManualMinGap + time.Second)
	third, err := f.srv.syncTrackerInbound(context.Background(), "manual")
	if err != nil || third.Coalesced {
		t.Fatalf("third after the window: %+v %v", third, err)
	}
}

// denyInbound makes the database refuse the given statement kind through a
// trigger created on a second connection.
func denyInbound(t *testing.T, f *inboundFixture, trigger string) {
	t.Helper()
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(trigger); err != nil {
		t.Fatal(err)
	}
}

func TestInboundLaunchIsNotStartedWhenTheAuditEventCannotBeWritten(t *testing.T) {
	f := newInboundFixtureOn(t, true, defaultInboundRules)
	f.task(t, "inbound-noaudit", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	denyInbound(t, f, `CREATE TRIGGER deny_launch BEFORE INSERT ON events WHEN NEW.type = 'tracker_inbound_launch'
		BEGIN SELECT RAISE(ABORT, 'journal unavailable'); END`)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "pat"); err != nil {
		t.Fatal(err)
	}
	if result := f.sync(t); result.Applied != 0 || result.Pending != 1 {
		t.Fatalf("result: %+v", result)
	}
	if len(f.controller.agentCalls) != 0 {
		t.Fatalf("a stage started without its audit record: %v", f.controller.agentCalls)
	}
	open := f.requests(t, "pending")
	if len(open) != 1 || !strings.Contains(open[0].Reason, "could not be recorded in the run journal") {
		t.Fatalf("request must stay pending with a reason: %+v", open)
	}
}

func TestInboundLaunchIsNotStartedWhenTheRequestCannotBePersisted(t *testing.T) {
	f := newInboundFixtureOn(t, true, defaultInboundRules)
	f.task(t, "inbound-nopersist", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	denyInbound(t, f, `CREATE TRIGGER deny_resolve BEFORE UPDATE ON tracker_inbound_changes
		BEGIN SELECT RAISE(ABORT, 'inbound storage unavailable'); END`)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "pat"); err != nil {
		t.Fatal(err)
	}
	// The pass fails as a whole (the outcome cannot be stored either) and
	// the controller is never called.
	if _, err := f.srv.syncTrackerInbound(context.Background(), "test"); err == nil {
		t.Fatal("storage failure must fail the pass so the cursor stays")
	}
	if len(f.controller.agentCalls) != 0 {
		t.Fatalf("a stage started although the request could not be persisted: %v", f.controller.agentCalls)
	}
	if launches := inboundEvents(t, f, "inbound-nopersist", trackerInboundLaunchEvent); len(launches) != 0 {
		t.Fatalf("launch audit written without a persisted request: %+v", launches)
	}
}
