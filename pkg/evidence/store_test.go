package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func testRunManifest(runID string) RunManifest {
	return RunManifest{RunID: runID, ConfigSnapshot: json.RawMessage(`{"schema_version":1}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`)}
}

type countingEventLog struct {
	delegate eventLog
	reads    int
	appends  int
}

func (l *countingEventLog) Read(runID string) ([]Event, error) {
	l.reads++
	return l.delegate.Read(runID)
}

func (l *countingEventLog) ReadBytes(runID string) ([]byte, error) {
	return l.delegate.ReadBytes(runID)
}

func (l *countingEventLog) Append(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	l.appends++
	return l.delegate.Append(runID, event, expectedSequence, expectedPreviousSHA256)
}

func (l *countingEventLog) AppendControllerEvent(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	controllerAppender, ok := l.delegate.(controllerOnlyEventAppender)
	if !ok {
		return Event{}, errors.New("test event log delegate has no controller append")
	}
	l.appends++
	return controllerAppender.AppendControllerEvent(runID, event, expectedSequence, expectedPreviousSHA256)
}

func TestStoreUsesInternalEventLogSeamForAppendAndResume(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	manifest := testRunManifest("run-event-log-port")
	journalPath := filepath.Join(root, manifest.RunID, "events.jsonl")
	startLog := &countingEventLog{delegate: newFileEventLog(journalPath)}
	store, err := startWithEventLog(root, manifest, startLog)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if err := store.Append(Event{Type: "run_started", Timestamp: startedAt}); err != nil {
		t.Fatal(err)
	}
	if startLog.appends != 1 {
		t.Fatalf("injected event log append calls=%d, want 1", startLog.appends)
	}

	resumeLog := &countingEventLog{delegate: newFileEventLog(journalPath)}
	resumed, _, replayed, err := resumeWithEventLog(root, manifest.RunID, resumeLog)
	if err != nil {
		t.Fatal(err)
	}
	if resumeLog.reads != 1 || replayed.StartedAt != startedAt {
		t.Fatalf("resume journal reads=%d replay=%+v", resumeLog.reads, replayed)
	}
	if err := resumed.Append(Event{Type: "run_resumed", Timestamp: startedAt.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if resumeLog.appends != 1 {
		t.Fatalf("resumed event log append calls=%d, want 1", resumeLog.appends)
	}

	events, err := VerifyEventLog(journalPath, manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "run_started" || events[1].Type != "run_resumed" || events[1].Sequence != 2 {
		t.Fatalf("filesystem journal did not preserve event sequence: %+v", events)
	}
	replayed, err = ReplayEventLog(journalPath, manifest.RunID)
	if err != nil || replayed.StartedAt != startedAt || replayed.RunID != manifest.RunID {
		t.Fatalf("filesystem replay=%+v err=%v", replayed, err)
	}
}

func TestControllerOnlyDescriptionMissingUsesValidatedLocalAppend(t *testing.T) {
	newRun := func(runID string) (*Store, time.Time) {
		store, err := Start(filepath.Join(t.TempDir(), "runs"), testRunManifest(runID))
		if err != nil {
			t.Fatal(err)
		}
		startedAt := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
		if err := store.Append(Event{Type: "run_started", Timestamp: startedAt}); err != nil {
			t.Fatal(err)
		}
		return store, startedAt
	}
	appendAttempt := func(t *testing.T, store *Store, startedAt time.Time, executor string) {
		t.Helper()
		data := map[string]any{"stage_index": 1, "executor": executor}
		if executor == "human" {
			data["actor_id"] = "writer-1"
			data["actor_role"] = "writer"
			data["human_input_approval_id"] = "approval-writer-1"
		}
		if err := store.Append(Event{Type: "attempt_started", Stage: "writer", AttemptID: "attempt-writer-1", Timestamp: startedAt.Add(time.Second),
			Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	warning := func(at time.Time) Event {
		return Event{Type: "description_missing", Stage: "writer", AttemptID: "attempt-writer-1", Timestamp: at,
			Data: map[string]any{"field": "description", "approval_id": "approval-writer-1"}}
	}

	t.Run("human attempt accepted, generic append rejected", func(t *testing.T) {
		store, startedAt := newRun("run-description-human")
		appendAttempt(t, store, startedAt, "human")
		if err := store.Append(warning(startedAt.Add(2 * time.Second))); err == nil {
			t.Fatal("generic event append accepted controller-only description_missing")
		}
		if err := store.AppendControllerEvent(warning(startedAt.Add(2 * time.Second))); err != nil {
			t.Fatalf("controller-only event append: %v", err)
		}
		events, err := VerifyEventLog(filepath.Join(store.RunDir(), "events.jsonl"), store.RunID())
		if err != nil || len(events) != 3 {
			t.Fatalf("read controller event: events=%d err=%v", len(events), err)
		}
		retried, err := newFileEventLog(filepath.Join(store.RunDir(), "events.jsonl")).AppendControllerEvent(
			store.RunID(), warning(startedAt.Add(2*time.Second)), 2, events[1].SHA256)
		if err != nil || retried.Sequence != events[2].Sequence || retried.SHA256 != events[2].SHA256 {
			t.Fatalf("controller append exact retry=%+v err=%v want %+v", retried, err, events[2])
		}
		if _, _, replayed, err := Resume(filepath.Dir(store.RunDir()), store.RunID()); err != nil || len(replayed.Attempts) != 1 || replayed.Attempts[0].Executor != "human" {
			t.Fatalf("resume after controller event: attempts=%+v err=%v", replayed.Attempts, err)
		}
	})

	t.Run("agent attempt rejected", func(t *testing.T) {
		store, startedAt := newRun("run-description-agent")
		appendAttempt(t, store, startedAt, "agent")
		if err := store.AppendControllerEvent(warning(startedAt.Add(2 * time.Second))); err == nil {
			t.Fatal("controller-only event accepted an agent attempt")
		}
	})
}

func skippedAttemptJournal(t *testing.T, executor, stageAction string, protocolVersion int, finishedReason, warningReason string) string {
	t.Helper()
	root := t.TempDir()
	if executor == "" {
		executor = "agent"
	}
	runID := "run-skip-replay-compat"
	artifactRoot := filepath.Join(root, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := testRunManifest(runID)
	manifest.TargetDir = root
	store, err := Start(filepath.Join(root, ".ai-team", "runs"), manifest)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "attempt-skipped"
	startedData := map[string]any{"stage_index": 1, "executor": executor}
	if executor == "human" {
		startedData["actor_id"], startedData["actor_role"], startedData["human_input_approval_id"] = "reviewer", "reviewer", "approval-skip"
	}
	if stageAction != "" {
		startedData["stage_action"] = stageAction
	}
	if protocolVersion != 0 {
		startedData["stage_skip_version"] = protocolVersion
	}
	if err := store.Append(Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID,
		Timestamp: started.Add(time.Second), Data: startedData}); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(2 * time.Second)
	attemptManifest := AttemptManifest{
		RunID: runID, AttemptID: attemptID, Stage: "optional", Executor: executor,
		StageIndex: 1, TotalStages: 1, StartedAt: started.Add(time.Second), FinishedAt: finished,
		Status: string(workflow.OutcomeSkipped), Execution: string(workflow.ExecutionSucceeded),
		Decision: string(workflow.DecisionNotApplicable), Outcome: string(workflow.OutcomeSkipped),
		Usage: &workflow.AttemptUsage{Attested: true},
	}
	if executor == "human" {
		attemptManifest.ActorID, attemptManifest.ActorRole, attemptManifest.HumanInputApprovalID = "reviewer", "reviewer", "approval-skip"
	}
	if err := store.PublishAttempt(attemptManifest, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}
	digest, _, err := AttemptManifestDigest(nil, store.RunDir(), runID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	finishedData := map[string]any{
		"status": string(workflow.OutcomeSkipped), "execution": workflow.ExecutionSucceeded,
		"decision": workflow.DecisionNotApplicable, "outcome": workflow.OutcomeSkipped,
		"executor": executor, "manifest_sha256": digest,
	}
	if executor == "human" {
		finishedData["actor_id"], finishedData["actor_role"], finishedData["human_input_approval_id"] = "reviewer", "reviewer", "approval-skip"
	}
	if finishedReason != "" {
		finishedData["stage_skip_reason"] = finishedReason
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID,
		Timestamp: finished, Data: finishedData}); err != nil {
		t.Fatal(err)
	}
	warningData := map[string]any{"reason": warningReason, "warning": true}
	if executor == "human" {
		warningData["actor_id"], warningData["actor_role"] = "reviewer", "reviewer"
	}
	if err := store.Append(Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID,
		Timestamp: finished, Data: warningData}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.RunDir(), "events.jsonl")
}

func TestReplayRequiresVersionedSkipReasonAndExactWarningMatch(t *testing.T) {
	for _, tc := range []struct {
		name           string
		finishedReason string
		warningReason  string
		wantErr        string
	}{
		{name: "missing durable reason", warningReason: "The optional stage is not needed.", wantErr: "no durable reason"},
		{name: "warning reason differs", finishedReason: "The optional stage is not needed.", warningReason: "A different skip reason.", wantErr: "reason differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := skippedAttemptJournal(t, "agent", "skip", StageSkipProtocolVersion, tc.finishedReason, tc.warningReason)
			if _, err := ReplayEventLog(path, "run-skip-replay-compat"); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("strict replay err=%v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestReplayAcceptsLegacyStageSkipWarningJournal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		executor    string
		stageAction string
	}{
		// c586db3's checked-in synthetic agent skip had stage_action but no
		// durable attempt_finished reason; its stage_skipped event is authority.
		{name: "c586db3 synthetic agent skip shape", executor: "agent", stageAction: "skip"},
		// c586db3 human skips carried the executor and actor identity, but no
		// stage_action or durable attempt_finished reason.
		{name: "c586db3 human skip shape", executor: "human"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := skippedAttemptJournal(t, tc.executor, tc.stageAction, 0, "", "The optional stage is not needed.")
			replayed, err := ReplayEventLog(path, "run-skip-replay-compat")
			if err != nil || len(replayed.StageSkips) != 1 || replayed.StageSkips[0].Reason != "The optional stage is not needed." {
				t.Fatalf("legacy skip should replay from its durable warning: replay=%+v err=%v", replayed, err)
			}
		})
	}

	t.Run("agent attempt without explicit skip action is rejected", func(t *testing.T) {
		path := skippedAttemptJournal(t, "agent", "", 0, "", "The optional stage is not needed.")
		if _, err := ReplayEventLog(path, "run-skip-replay-compat"); err == nil || !strings.Contains(err.Error(), "not an explicit stage skip") {
			t.Fatalf("unmarked agent attempt must not be accepted as a legacy skip: err=%v", err)
		}
	})
}

func TestCleanupInflightInputSnapshotsRemovesScratchWithoutFollowingLinks(t *testing.T) {
	target := t.TempDir()
	const runID = "run-inflight-cleanup"
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Start(runsRoot, testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "answer.md")
	if err := os.WriteFile(source, []byte("sensitive answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots, _, err := store.SnapshotInputs("crashed-attempt", []Artifact{{Name: "clarification-answer", Path: source}})
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot fixture: inputs=%+v err=%v", snapshots, err)
	}
	if _, err := os.Stat(snapshots[0].Path); err != nil {
		t.Fatalf("crash snapshot should exist before recovery: %v", err)
	}

	external := filepath.Join(t.TempDir(), "external-answer.md")
	if err := os.WriteFile(external, []byte("must not be followed"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(store.RunDir(), "inflight-inputs", "orphan-link")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInflightInputSnapshots(target, runID); err != nil {
		t.Fatalf("cleanup orphaned snapshots: %v", err)
	}
	if _, err := os.Lstat(snapshots[0].Path); !os.IsNotExist(err) {
		t.Fatalf("orphan snapshot remains after cleanup: %q err=%v", snapshots[0].Path, err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("orphan symlink remains after cleanup: %q err=%v", link, err)
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "must not be followed" {
		t.Fatalf("cleanup followed a symlink outside the scratch tree: %q err=%v", data, err)
	}
}

func TestDeliveryDeferredAdmissionRejectsForeignOrTraversingStatePath(t *testing.T) {
	target := t.TempDir()
	runID := "run-delivery-state-admission"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := testRunManifest(runID)
	manifest.TargetDir = target
	store, err := Start(filepath.Join(target, ".ai-team", "runs"), manifest)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-delivery-path"
	if err := store.Append(Event{Type: "attempt_started", Stage: "deployer", AttemptID: attemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(target, ".ai-team", "delivery", "prepared.json")
	if !ValidDeliveryStatePath(store.RunDir(), validPath) {
		t.Fatalf("expected prepared delivery path to match target: %q", validPath)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(canonicalTarget, ".ai-team", "worktrees", runID, ".ai-team", "delivery", "prepared.json")
	if !ValidDeliveryStatePath(store.RunDir(), candidatePath) {
		t.Fatalf("expected same-run candidate delivery path to match target: %q", candidatePath)
	}
	alias := filepath.Join(t.TempDir(), "target-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	aliasedRunDir := filepath.Join(alias, ".ai-team", "runs", runID)
	if !ValidDeliveryStatePath(aliasedRunDir, candidatePath) {
		t.Fatalf("expected local validation to tolerate target symlink spelling without resolving the event path: run=%q state=%q", aliasedRunDir, candidatePath)
	}
	invalidPaths := []string{
		filepath.Join(filepath.Dir(target), "foreign", "prepared.json"),
		filepath.Join(target, ".ai-team", "worktrees", "another-run", ".ai-team", "delivery", "prepared.json"),
		target + "/.ai-team/delivery/../runs/escaped.json",
		target + "/.ai-team/delivery/../delivery/prepared.json",
		target + "/.ai-team/worktrees/" + runID + "/.ai-team/delivery/../events.jsonl",
	}
	for _, statePath := range invalidPaths {
		t.Run(statePath, func(t *testing.T) {
			events, readErr := store.eventLog.Read(runID)
			if readErr != nil || len(events) != 2 {
				t.Fatalf("baseline events=%d err=%v", len(events), readErr)
			}
			_, _, err := ValidateEventAppend(events, runID, store.RunDir(), Event{Type: "delivery_deferred", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
				"plan_hash": strings.Repeat("a", 64), "state_path": filepath.ToSlash(statePath),
			}}, uint64(len(events)), events[len(events)-1].SHA256, nil)
			if err == nil {
				t.Fatalf("foreign delivery state path was accepted: %q", statePath)
			}
		})
	}
}

func TestFileEventLogSerializesStaleStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	manifest := testRunManifest("run-event-log-race")
	first, err := Start(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := Resume(root, manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func(store *Store) {
			<-start
			results <- store.Append(Event{Type: "run_started", Timestamp: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)})
		}(store)
	}
	close(start)
	errorsSeen := 0
	for range 2 {
		if err := <-results; err != nil {
			errorsSeen++
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("stale writer errors=%d, want exactly 1", errorsSeen)
	}
	events, err := VerifyEventLog(filepath.Join(first.RunDir(), "events.jsonl"), manifest.RunID)
	if err != nil {
		t.Fatalf("serialized journal is invalid: %v", err)
	}
	if len(events) != 1 || events[0].Type != "run_started" || events[0].Sequence != 1 {
		t.Fatalf("unexpected journal after concurrent append: %+v", events)
	}
}

func TestFileEventLogPropagatesCloseErrorAfterSync(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	manifest := testRunManifest("run-event-log-close")
	path := filepath.Join(root, manifest.RunID, "events.jsonl")
	closeFailure := errors.New("injected close failure")
	closeCalls := 0
	log := &fileEventLog{path: path, closeFile: func(file *os.File) error {
		closeCalls++
		_ = file.Close()
		return closeFailure
	}}
	store, err := startWithEventLog(root, manifest, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}); !errors.Is(err, closeFailure) {
		t.Fatalf("Append error=%v, want close error", err)
	}
	if closeCalls != 1 {
		t.Fatalf("event file close calls=%d, want exactly 1", closeCalls)
	}
	events, err := VerifyEventLog(path, manifest.RunID)
	if err != nil || len(events) != 1 || events[0].Type != "run_started" {
		t.Fatalf("event after synced close failure: events=%+v err=%v", events, err)
	}
}

func TestValidateRunIDRejectsControlAndPlatformUnsafeValues(t *testing.T) {
	for _, runID := range []string{"", ".", "..", "../outside", `a\b`, strings.Repeat("x", 256), "bad\tvalue", "bad\x7fvalue", "bad\x01value"} {
		if err := ValidateRunID(runID); err == nil {
			t.Errorf("ValidateRunID accepted %q", runID)
		}
	}
	for _, runID := range []string{"run-1", "2026-10-07T12:00:00Z-run"} {
		if err := ValidateRunID(runID); err != nil {
			t.Errorf("ValidateRunID rejected valid run id %q: %v", runID, err)
		}
	}
}

func TestStorePublishesImmutableAttemptWithHashes(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	input := filepath.Join(artifactRoot, "feature", "input.md")
	outputDir := filepath.Join(artifactRoot, "feature", "specs")
	output := filepath.Join(outputDir, "spec.md")
	for path, content := range map[string]string{input: "input-v1", output: "output-v1"} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	runManifest := testRunManifest("run-1")
	runManifest.Feature, runManifest.TargetDir, runManifest.StartedAt = "feature", target, time.Now()
	store, err := Start(filepath.Join(target, "runs"), runManifest)
	if err != nil {
		t.Fatal(err)
	}
	attempt := AttemptManifest{
		AttemptID: "001-analyst", Stage: "analyst", StageIndex: 1,
		StartedAt: time.Now(), FinishedAt: time.Now(), Status: "passed",
	}
	if err := store.PublishAttempt(attempt, artifactRoot,
		[]Artifact{{Name: "input", Path: input}},
		[]Artifact{{Name: "specs", Path: outputDir}, {Name: "spec", Path: output}},
	); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(store.RunDir(), "attempts", attempt.AttemptID, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || len(manifest.Inputs) != 1 || len(manifest.Outputs) != 2 {
		t.Fatalf("неполный manifest: %+v", manifest)
	}
	if inputRecord := manifest.Inputs[0]; len(inputRecord.SHA256) != 64 || inputRecord.ConsumedByRunID != "run-1" ||
		inputRecord.ConsumedByAttemptID != attempt.AttemptID || !inputRecord.ExternalOrLegacyInput || inputRecord.EvidencePath == "" {
		t.Errorf("невалидная input provenance record: %+v", inputRecord)
	}
	for _, record := range manifest.Outputs {
		if len(record.SHA256) != 64 || record.ProducerRunID != "run-1" || record.ProducerAttemptID != attempt.AttemptID || record.ProducerStage != "analyst" {
			t.Errorf("невалидная output provenance record: %+v", record)
		}
	}

	evidenceOutput := filepath.Join(store.RunDir(), filepath.FromSlash(manifest.Outputs[1].EvidencePath))
	if err := os.WriteFile(output, []byte("output-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	immutable, err := os.ReadFile(evidenceOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(immutable) != "output-v1" {
		t.Fatalf("evidence изменился вместе с live artifact: %q", immutable)
	}
	evidenceInput := filepath.Join(store.RunDir(), filepath.FromSlash(manifest.Inputs[0].EvidencePath))
	if err := os.WriteFile(input, []byte("input-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	immutableInput, err := os.ReadFile(evidenceInput)
	if err != nil || string(immutableInput) != "input-v1" {
		t.Fatalf("immutable input copy: %q err=%v", immutableInput, err)
	}
}

func TestRunManifestBindsExactSnapshotsAndController(t *testing.T) {
	target := t.TempDir()
	manifest := testRunManifest("run-manifest")
	store, err := Start(filepath.Join(target, "runs"), manifest)
	if err != nil {
		t.Fatal(err)
	}

	rawManifest, err := os.ReadFile(filepath.Join(store.RunDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded RunManifest
	if err := json.Unmarshal(rawManifest, &recorded); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(store.RunDir(), recorded.ConfigEvidence))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join(store.RunDir(), recorded.ResolvedWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	if recorded.SchemaVersion != SchemaVersion || recorded.ConfigSHA256 != sha256Bytes(config) ||
		recorded.ResolvedWorkflowSHA256 != sha256Bytes(workflow) {
		t.Fatalf("manifest does not bind exact snapshots: %+v", recorded)
	}
	if len(recorded.Controller.ExecutableSHA256) != 64 || recorded.Controller.GoVersion == "" ||
		recorded.Controller.GOOS == "" || recorded.Controller.GOARCH == "" {
		t.Fatalf("controller identity is incomplete: %+v", recorded.Controller)
	}
}

func TestEventLogHashChainDetectsTampering(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"run_started", "run_finished"} {
		if err := store.Append(Event{Type: eventType, Timestamp: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(store.RunDir(), "events.jsonl")
	events, err := VerifyEventLog(path, "run-events")
	if err != nil || len(events) != 2 || events[0].PreviousSHA256 != chainGenesis("run-events") ||
		events[1].PreviousSHA256 != events[0].SHA256 {
		t.Fatalf("valid event chain: events=%+v err=%v", events, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"type":"run_started"`, `"type":"run_changed"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEventLog(path, "run-events"); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered event chain must fail, got %v", err)
	}
	if err := store.Append(Event{Type: "after_tamper"}); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("store must refuse appending to tampered log, got %v", err)
	}
}

func TestResumeContinuesVerifiedNonTerminalChain(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(target, "runs")
	manifest := testRunManifest("run-resume")
	manifest.Feature, manifest.TargetDir, manifest.StartedAt = "feature", target, time.Now().UTC()
	store, err := Start(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: manifest.StartedAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_paused", Timestamp: manifest.StartedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	resumed, loaded, replayed, err := Resume(root, "run-resume")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != "run-resume" || !replayed.FinishedAt.IsZero() {
		t.Fatalf("unexpected resume state: manifest=%+v replay=%+v", loaded, replayed)
	}
	if err := resumed.Append(Event{Type: "run_resumed", Timestamp: manifest.StartedAt.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Append(Event{Type: "run_finished", Timestamp: manifest.StartedAt.Add(3 * time.Second), Data: map[string]any{
		"status": "failed", "stage_attempts": 0,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Resume(root, "run-resume"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("terminal resume должен быть отклонён: %v", err)
	}
}

// TestEventLogRejectsSplicedEventFromAnotherChain covers what per-event
// digest recomputation alone cannot: an event that is individually
// self-consistent (its own stored SHA256 still matches its own content,
// and its own PreviousSHA256 still matches whatever it originally followed)
// but was spliced in after a *different* preceding event than the one it
// actually followed. Sequence numbers alone don't catch this either, since
// both chains number their events identically. Only the previous_sha256
// link check (comparing against the actual preceding event in this file,
// not the event's own stored belief about its predecessor) catches it.
func TestEventLogRejectsSplicedEventFromAnotherChain(t *testing.T) {
	target := t.TempDir()

	storeA, err := Start(filepath.Join(target, "runs-a"), testRunManifest("run-x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storeA.Append(Event{Type: "run_started_chain_a", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	storeB, err := Start(filepath.Join(target, "runs-b"), testRunManifest("run-x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storeB.Append(Event{Type: "run_started_chain_b", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := storeB.Append(Event{Type: "stage_started", Stage: "coder", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	pathA := filepath.Join(storeA.RunDir(), "events.jsonl")
	pathB := filepath.Join(storeB.RunDir(), "events.jsonl")
	dataA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	linesA := strings.Split(strings.TrimRight(string(dataA), "\n"), "\n")
	linesB := strings.Split(strings.TrimRight(string(dataB), "\n"), "\n")
	if len(linesA) != 1 || len(linesB) != 2 {
		t.Fatalf("unexpected line counts: A=%d B=%d", len(linesA), len(linesB))
	}

	// Splice chain A's real first event with chain B's real, unmodified
	// second event. Both are individually valid (correct own digest,
	// correct own sequence for position 2, same RunID "run-x"), but B's
	// second event never actually followed A's first event.
	spliced := linesA[0] + "\n" + linesB[1] + "\n"
	if err := os.WriteFile(pathA, []byte(spliced), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyEventLog(pathA, "run-x"); err == nil {
		t.Fatal("event spliced in from an unrelated chain must be rejected")
	}
}

func TestReplayEventLogReconstructsAttemptsAndVerifiesManifest(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-replay"))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	attemptID := "run-replay-001-check"
	if err := store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: attemptID, Stage: "check", StageIndex: 1, StartedAt: started.Add(time.Second),
		FinishedAt: started.Add(2 * time.Second), Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, _, manifestDigest, err := ArtifactDigest(filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{
		"status": "passed", "execution": "succeeded", "decision": "approved", "outcome": "passed", "verdict": "PASS", "manifest_sha256": manifestDigest,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempts_invalidated", Timestamp: started.Add(3 * time.Second), Data: map[string]any{"attempt_ids": []string{attemptID}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_finished", Timestamp: started.Add(4 * time.Second), Data: map[string]any{"status": "completed", "stage_attempts": 1}}); err != nil {
		t.Fatal(err)
	}
	replayed, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-replay")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != "completed" || replayed.LastEventSHA256 == "" || len(replayed.Attempts) != 1 ||
		!replayed.Attempts[0].Superseded || replayed.Attempts[0].Status != "invalidated" || replayed.Attempts[0].ManifestSHA256 != manifestDigest {
		t.Fatalf("unexpected replay projection: %+v", replayed)
	}

	manifestPath := filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json")
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-replay"); err == nil || !strings.Contains(err.Error(), "manifest identity mismatch") {
		t.Fatalf("tampered attempt manifest must invalidate replay: %v", err)
	}
}

func TestReplayEventLogRejectsImpossibleTransition(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-invalid-replay"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "ghost", AttemptID: "ghost-1", Timestamp: time.Now().UTC(), Data: map[string]any{
		"status": "failed", "execution": "infra_failed", "decision": "not_applicable", "outcome": "failed", "error": "missing start",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(filepath.Join(store.RunDir(), "events.jsonl"), "run-invalid-replay"); err == nil || !strings.Contains(err.Error(), "no matching active attempt") {
		t.Fatalf("impossible event transition must be rejected: %v", err)
	}
}

func TestStoreLinksCurrentRunProducerToConsumer(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	artifact := filepath.Join(artifactRoot, "feature", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(artifact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("proposal"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-001-analyst", Stage: "analyst"}, artifactRoot, nil,
		[]Artifact{{Name: "proposal", Path: artifact}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-002-reviewer", Stage: "reviewer"}, artifactRoot,
		[]Artifact{{Name: "proposal", Path: artifact}}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunDir(), "attempts", "run-1-002-reviewer", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	input := manifest.Inputs[0]
	if input.ProducerRunID != "run-1" || input.ProducerAttemptID != "run-1-001-analyst" || input.ProducerStage != "analyst" || input.ExternalOrLegacyInput {
		t.Fatalf("producer lineage missing: %+v", input)
	}
}

func TestStoreRejectsInputBytesThatNoLongerMatchProducer(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	artifact := filepath.Join(artifactRoot, "feature", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(artifact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("producer-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{AttemptID: "run-1-001-producer", Stage: "producer"}, artifactRoot, nil,
		[]Artifact{{Name: "proposal", Path: artifact}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	err = store.PublishAttempt(AttemptManifest{AttemptID: "run-1-002-consumer", Stage: "consumer"}, artifactRoot,
		[]Artifact{{Name: "proposal", Path: artifact}}, nil)
	if err == nil || !strings.Contains(err.Error(), "provenance mismatch") {
		t.Fatalf("tampered live artifact must not inherit producer identity: %v", err)
	}
}

func TestStoreRejectsSymlinkOutsideArtifactRoot(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(target, "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(artifactRoot, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.PublishAttempt(AttemptManifest{AttemptID: "001-stage", Stage: "stage"}, artifactRoot, nil,
		[]Artifact{{Name: "link", Path: link}})
	if err == nil || !strings.Contains(err.Error(), "вне artifact root") {
		t.Fatalf("symlink outside должен быть отклонён: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(store.RunDir(), "attempts", "001-stage")); !os.IsNotExist(statErr) {
		t.Fatalf("неуспешная публикация не должна оставлять final attempt: %v", statErr)
	}
}

func TestWorkspaceLockIsExclusiveAndReleasable(t *testing.T) {
	target := t.TempDir()
	first, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("второй lock должен быть отклонён")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatalf("lock должен переиспользоваться после release: %v", err)
	}
	_ = third.Close()
}

func TestWorkspaceLockRejectsSymlinkFile(t *testing.T) {
	target := t.TempDir()
	initial, err := AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(target, ".ai-team", "locks", "workspace.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, lockPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := AcquireWorkspaceLock(target); err == nil {
		t.Fatal("lock symlink must fail closed")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("victim must not be truncated: %q err=%v", data, err)
	}
}

func TestRunIDIsSortableByTimestamp(t *testing.T) {
	first, err := NewRunID(time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRunID(time.Date(2026, 7, 19, 1, 2, 4, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first >= second {
		t.Fatalf("run IDs должны сортироваться по timestamp: %s >= %s", first, second)
	}
}
