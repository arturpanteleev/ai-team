package gatesched

import (
	"context"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// recorded_waves_phase_test.go — finding E: RecordedWaves includes the
// effective phase in the wave key, so an entry chain and an exit chain over the
// same scope and hash fold into two independent waves and expose the phase.

func TestRecordedWavesSeparatesEntryAndExitPhases(t *testing.T) {
	log := &fakeLog{}
	sink := EventSink{Read: log.read, Append: log.append}
	groups := workflow.GateGroups{{ref("shared")}}
	definitions := defs(def("shared", workflow.GateKindBuiltin))
	runner := RunnerFunc(func(context.Context, Invocation) Outcome { return Pass("ok") })

	entry, err := GuardExecute(context.Background(),
		newScheduler(t, NewEventJournal(sink), Options{}, runner),
		entryWave(groups, definitions), func(context.Context) error { return nil })
	if err != nil || !entry.Allowed {
		t.Fatalf("entry wave: %v %+v", err, entry)
	}
	if _, err := newScheduler(t, NewEventJournal(sink), Options{}, runner).Run(
		context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}

	if len(log.events) == 0 {
		t.Fatal("no attempt events recorded")
	}
	var events []evidence.Event
	for _, event := range log.events {
		if isAttemptEvent(event.Type) {
			events = append(events, event)
		}
	}
	waves, err := RecordedWaves(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 2 {
		t.Fatalf("entry and exit must fold into two waves, got %d: %+v", len(waves), waves)
	}
	phases := map[workflow.GatePhase]bool{}
	for _, wave := range waves {
		phases[wave.Phase] = true
		if wave.Phase != wave.Key.Phase.Effective() {
			t.Fatalf("wave phase %q must match its key %q", wave.Phase, wave.Key.Phase)
		}
		if len(wave.Attempts) != 1 {
			t.Fatalf("each wave must hold its own single attempt: %+v", wave.Attempts)
		}
		if wave.Attempts[0].GateName != "shared" {
			t.Fatalf("unexpected gate in wave: %q", wave.Attempts[0].GateName)
		}
	}
	if !phases[workflow.GatePhaseEntry] || !phases[workflow.GatePhaseExit] {
		t.Fatalf("both phases must be reported: %+v", phases)
	}
}
