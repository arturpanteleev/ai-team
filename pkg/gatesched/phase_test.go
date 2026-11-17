package gatesched

import (
	"context"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// phase_test.go — finding 4: the phase is part of the wave identity. An entry
// wave and an exit wave over the same task, stage, visit, scope, hash and gate
// name run separately with distinct attempt IDs and histories, and a restart of
// each phase reuses only its own attempts. Old exit records with an empty phase
// keep matching the exit chain.

func TestEntryAndExitWavesRunSeparatelyWithDistinctAttempts(t *testing.T) {
	journal := &MemoryJournal{}
	groups := workflow.GateGroups{{ref("shared")}}
	definitions := defs(def("shared", workflow.GateKindBuiltin))
	calls := newCounters()
	runner := RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		return Pass("ok")
	})

	entryResult, err := GuardExecute(context.Background(), newScheduler(t, journal, Options{}, runner), entryWave(groups, definitions), func(context.Context) error { return nil })
	if err != nil || !entryResult.Allowed {
		t.Fatalf("entry wave: %v %+v", err, entryResult)
	}
	if _, err := newScheduler(t, journal, Options{}, runner).Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}
	if calls.count("shared") != 2 {
		t.Fatalf("entry and exit waves must run separately over the same hash: calls=%d", calls.count("shared"))
	}

	// Restarting each phase reuses its own attempts and invokes nothing new.
	restartEntry, err := GuardExecute(context.Background(), newScheduler(t, journal, Options{}, runner), entryWave(groups, definitions), func(context.Context) error { return nil })
	if err != nil || !restartEntry.Allowed {
		t.Fatalf("entry restart: %v %+v", err, restartEntry)
	}
	if _, err := newScheduler(t, journal, Options{}, runner).Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}
	if calls.count("shared") != 2 {
		t.Fatalf("restarts must reuse only their own phase attempts: calls=%d", calls.count("shared"))
	}

	var entryIDs, exitIDs []string
	for _, rec := range journal.Records() {
		if rec.Type != RecordFinished || rec.Attempt.GateName != "shared" {
			continue
		}
		if rec.Attempt.Phase.Effective() == workflow.GatePhaseEntry {
			entryIDs = append(entryIDs, rec.Attempt.AttemptID)
		} else {
			exitIDs = append(exitIDs, rec.Attempt.AttemptID)
		}
	}
	if len(entryIDs) == 0 || len(exitIDs) == 0 {
		t.Fatalf("both phases must be recorded: entry=%v exit=%v", entryIDs, exitIDs)
	}
	for _, entry := range entryIDs {
		for _, exit := range exitIDs {
			if entry == exit {
				t.Fatalf("entry and exit attempts must have distinct IDs: %q", entry)
			}
		}
	}
}

// An exit attempt recorded before the phase existed (empty phase) keeps its old
// ID and still satisfies the exit wave: the historical format is preserved.
func TestExitAttemptIDIsUnchangedAndMatchesEmptyPhase(t *testing.T) {
	k := key(hashV1)
	old := AttemptID(k, "shared", 1)
	if k.Phase != "" {
		t.Fatalf("the test key must carry an empty phase")
	}
	explicit := AttemptID(WaveKey{TaskID: k.TaskID, Stage: k.Stage, Visit: k.Visit, Scope: k.Scope, ArtifactSHA256: k.ArtifactSHA256, Phase: workflow.GatePhaseExit}, "shared", 1)
	if old != explicit {
		t.Fatalf("an empty phase must keep the historical exit ID: %q vs %q", old, explicit)
	}
	if !(WaveKey{}).matches(WaveKey{Phase: workflow.GatePhaseExit}) {
		t.Fatal("an empty phase must match the exit phase for recovery/fold")
	}
	if (WaveKey{Phase: workflow.GatePhaseEntry}).matches(WaveKey{Phase: workflow.GatePhaseExit}) {
		t.Fatal("entry and exit must never match")
	}
}
