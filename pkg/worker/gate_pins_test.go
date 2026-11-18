package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gatePinsFixture is a controller-owned config snapshot with exactly one
// pinned gate definition, matching what the pipeline records at task start.
func gatePinsFixture(t *testing.T, runID, target string) (json.RawMessage, []workflow.GatePin, evidence.EventLog) {
	t.Helper()
	cfg := config.Config{
		Template: "gate-pins-fixture",
		Stages: []config.TemplateStage{{
			ID: "writer",
			ExitGateGroups: workflow.GateGroups{{
				workflow.GateRef{Gate: "required_sections", Version: 1, Params: map[string]any{"sections": []any{"A"}}},
			}},
		}},
	}
	snapshot, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pins, err := cfg.GatePins()
	if err != nil || len(pins) != 1 {
		t.Fatalf("fixture pins: pins=%+v err=%v", pins, err)
	}
	digest := sha256.Sum256(snapshot)
	states, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := states.Create(lifecycle.State{
		RunID: runID, Feature: "gate-pins", TargetDir: target, Task: "bind gate pins",
		Phase: lifecycle.PhaseRunning, NextStage: "writer", CreatedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
		ConfigSHA256: hex.EncodeToString(digest[:]), WorkflowSHA256: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	log := evidence.ControllerEventStore{TargetDir: target}
	if err := log.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	return snapshot, pins, log
}

func startGatePinsStore(t *testing.T, runID, target string, snapshot json.RawMessage, source evidence.EventLog) *evidence.Store {
	t.Helper()
	started := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	store, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "gate-pins", TargetDir: target, StartedAt: started,
		ConfigSnapshot: snapshot, WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestWorkerAPIGatePinsAreControllerOwned(t *testing.T) {
	const runID = "gate-pins-controller"
	target := t.TempDir()
	snapshot, pins, log := gatePinsFixture(t, runID, target)
	startGatePinsStore(t, runID, target, snapshot, log)
	states, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	events, err := log.Read(runID)
	if err != nil || len(events) != 1 {
		t.Fatalf("fixture log: events=%d err=%v", len(events), err)
	}
	server := &workerAPIServer{
		scope:        workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target},
		usageAllowed: true, eventLogs: log, lifecycle: states,
	}

	// The generic worker append boundary stays closed for extension events.
	forged := evidence.Event{
		Type: workerGatePinsEventType, Timestamp: startedAt.Add(time.Second),
		Data: map[string]any{"template": "forged", "pins": []any{}},
	}
	if _, err := server.dispatch("event_log.append", workerAPICall{
		RunID: runID, Event: forged, ExpectedSequence: 1, ExpectedPreviousSHA256: events[0].SHA256,
	}); err == nil || !strings.Contains(err.Error(), "not allowed through worker API") {
		t.Fatalf("generic worker append accepted gate pins: %v", err)
	}

	// The dedicated controller method ignores worker-supplied pin data and
	// rebuilds the record from the verified pinned config.
	result, err := server.dispatch("event_log.gate_pins", workerAPICall{
		RunID: runID, Event: evidence.Event{Timestamp: startedAt.Add(time.Second)},
		ExpectedSequence: 1, ExpectedPreviousSHA256: events[0].SHA256,
	})
	if err != nil {
		t.Fatalf("controller gate pin append: %v", err)
	}
	receipt := result.(workerAPIEventAppendResult)
	if receipt.Type != workerGatePinsEventType || receipt.Sequence != 2 {
		t.Fatalf("gate pin receipt=%+v", receipt)
	}
	stored, err := log.Read(runID)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored log: events=%d err=%v", len(stored), err)
	}
	if stored[1].Data["template"] != "gate-pins-fixture" {
		t.Fatalf("template was not controller-owned: %+v", stored[1].Data)
	}
	recorded, _ := stored[1].Data["pins"].([]any)
	if len(recorded) != 1 {
		t.Fatalf("recorded pins=%+v", recorded)
	}
	pin, _ := recorded[0].(map[string]any)
	if pin["gate"] != pins[0].Gate || pin["digest"] != pins[0].Digest {
		t.Fatalf("recorded pin=%+v want %+v", pin, pins[0])
	}

	// A redelivered request is idempotent, not a duplicate append.
	retry, err := server.dispatch("event_log.gate_pins", workerAPICall{
		RunID: runID, Event: evidence.Event{Timestamp: startedAt.Add(time.Second)},
		ExpectedSequence: 1, ExpectedPreviousSHA256: events[0].SHA256,
	})
	if err != nil || retry.(workerAPIEventAppendResult).SHA256 != receipt.SHA256 {
		t.Fatalf("gate pin retry: result=%+v err=%v", retry, err)
	}
}

func TestWorkerAPIGatePinsEventLogUsesDedicatedRPC(t *testing.T) {
	const runID = "gate-pins-rpc"
	target := t.TempDir()
	snapshot, pins, _ := gatePinsFixture(t, runID, target)
	states, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "ai-team-gate-pins-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: runID, TargetDir: target, ExecutionID: strings.Repeat("e", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, &apiApprovalStore{}, filepath.Join(socketDir, "controller.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	server.lifecycle = states
	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, server.socketPath)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	eventLog := NewWorkerAPIEventLog(port)
	started := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	store, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "gate-pins-rpc", TargetDir: target, StartedAt: started,
		ConfigSnapshot: snapshot, WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, eventLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	forged := evidence.Event{
		Type: workerGatePinsEventType, Timestamp: started.Add(time.Second),
		Data: map[string]any{"template": "forged", "pins": []any{}},
	}
	if err := store.Append(forged); err != nil {
		t.Fatalf("worker gate pins append: %v", err)
	}
	events, err := server.eventLogs.Read(runID)
	if err != nil || len(events) != 2 {
		t.Fatalf("canonical log: events=%d err=%v", len(events), err)
	}
	recorded, _ := events[1].Data["pins"].([]any)
	if len(recorded) != 1 {
		t.Fatalf("recorded pins=%+v", recorded)
	}
	pin, _ := recorded[0].(map[string]any)
	if events[1].Data["template"] != "gate-pins-fixture" || pin["digest"] != pins[0].Digest {
		t.Fatalf("worker payload reached the durable log: %+v", events[1].Data)
	}
}

func TestWorkerAPIGatePinsRejectsUntrustedState(t *testing.T) {
	const runID = "gate-pins-untrusted"
	target := t.TempDir()
	snapshot, _, log := gatePinsFixture(t, runID, target)
	startGatePinsStore(t, runID, target, snapshot, log)
	states, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(runID)
	if err != nil || len(events) != 1 {
		t.Fatalf("fixture log: events=%d err=%v", len(events), err)
	}
	server := &workerAPIServer{
		scope:        workerAPIScope{RunID: runID, Operation: OperationCancel, TargetDir: target},
		usageAllowed: true, eventLogs: log, lifecycle: states,
	}
	call := workerAPICall{RunID: runID, ExpectedSequence: 1, ExpectedPreviousSHA256: events[0].SHA256}
	if _, err := server.dispatch("event_log.gate_pins", call); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("gate pin append allowed for cancel operation: %v", err)
	}
	server.scope.Operation = OperationStart
	if _, err := server.dispatch("event_log.gate_pins", workerAPICall{
		RunID: "other-run", ExpectedSequence: 1, ExpectedPreviousSHA256: events[0].SHA256,
	}); err == nil || !strings.Contains(err.Error(), "run mismatch") {
		t.Fatalf("gate pin append allowed for another run: %v", err)
	}
	server.lifecycle = nil
	if _, err := server.dispatch("event_log.gate_pins", call); err == nil {
		t.Fatal("gate pin append allowed without controller lifecycle state")
	}
}
