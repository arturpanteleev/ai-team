package gatesched

import (
	"context"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// entry_test.go — ARC-02 acceptance: a failed required entry gate must not
// start the executor, while a passing chain must. The guard keeps the same
// group semantics as the exit scheduler and records every attempt with the
// entry phase.

func entryWave(groups workflow.GateGroups, definitions map[string]workflow.GateDefinition) Wave {
	return Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
}

func TestEntryGateBlocksExecutorWhenRequiredGateFails(t *testing.T) {
	journal := &MemoryJournal{}
	groups := workflow.GateGroups{{ref("entry_check")}}
	definitions := defs(def("entry_check", workflow.GateKindBuiltin))
	s := newScheduler(t, journal, Options{}, RunnerFunc(func(context.Context, Invocation) Outcome {
		return Fail("вход не прошёл проверку")
	}))
	executed := false
	result, err := GuardExecute(context.Background(), s, entryWave(groups, definitions), func(context.Context) error {
		executed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed || result.Allowed {
		t.Fatalf("failed entry gate must block the executor: executed=%v result=%+v", executed, result)
	}
	if result.Status != StatusFailed || len(result.Blocking) != 1 {
		t.Fatalf("blocking result: %+v", result)
	}
	if !strings.Contains(result.BlockReason(), "fail") {
		t.Fatalf("block reason must name the verdict: %q", result.BlockReason())
	}
	records := journal.Records()
	if len(records) == 0 {
		t.Fatal("the failed attempt must be recorded in the journal")
	}
	var foundFinished bool
	for _, rec := range records {
		if rec.Type == RecordFinished && rec.Attempt.Verdict == workflow.GateVerdictFail {
			foundFinished = true
			if rec.Attempt.Phase != workflow.GatePhaseEntry {
				t.Fatalf("entry attempt must carry the entry phase: %+v", rec.Attempt)
			}
		}
	}
	if !foundFinished {
		t.Fatalf("fail verdict missing from journal: %+v", records)
	}
}

func TestEntryGateRunsExecutorWhenAllGroupsPass(t *testing.T) {
	calls := newCounters()
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		return Pass("ok")
	}))
	groups, definitions := chainAB_C()
	executed := 0
	result, err := GuardExecute(context.Background(), s, entryWave(groups, definitions), func(context.Context) error {
		executed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed || result.Status != StatusPassed || executed != 1 {
		t.Fatalf("passing entry chain must run the executor once: %+v executed=%d", result, executed)
	}
	for _, name := range []string{"a", "b", "c"} {
		if calls.count(name) != 1 {
			t.Fatalf("gate %s ran %d times", name, calls.count(name))
		}
	}
}

func TestEntryGroupFailureStopsLaterGroupsAndExecutor(t *testing.T) {
	calls := newCounters()
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "a" {
			return Fail("first group failed")
		}
		return Pass("ok")
	}))
	groups, definitions := chainAB_C()
	executed := false
	result, err := GuardExecute(context.Background(), s, entryWave(groups, definitions), func(context.Context) error {
		executed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed || result.Allowed {
		t.Fatalf("executor must be blocked: executed=%v %+v", executed, result)
	}
	if calls.count("a") != 1 || calls.count("b") != 1 || calls.count("c") != 0 {
		t.Fatalf("group order broken: %+v", calls.calls)
	}
	if len(result.NotStarted) != 1 || result.NotStarted[0] != "c" {
		t.Fatalf("dependent gate must be listed as not started: %+v", result.NotStarted)
	}
}

func TestEntryGateTechnicalErrorAlsoBlocksExecutor(t *testing.T) {
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(context.Context, Invocation) Outcome {
		return TechnicalError("gate runtime unavailable")
	}))
	groups := workflow.GateGroups{{ref("entry_check")}}
	executed := false
	result, err := GuardExecute(context.Background(), s, entryWave(groups, defs(def("entry_check", workflow.GateKindBuiltin))), func(context.Context) error {
		executed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed || result.Allowed || result.Status != StatusError {
		t.Fatalf("technical error must block: executed=%v %+v", executed, result)
	}
}

func TestNoEntryGatesRunsExecutor(t *testing.T) {
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(context.Context, Invocation) Outcome {
		return Pass("unexpected")
	}))
	executed := 0
	result, err := GuardExecute(context.Background(), s, entryWave(nil, nil), func(context.Context) error {
		executed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed || result.Status != "" || executed != 1 {
		t.Fatalf("no entry gates must keep the historical behavior: %+v executed=%d", result, executed)
	}
}
