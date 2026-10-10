package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func workerHumanSkipPinnedRun(t *testing.T, runID string, value *approval.PendingApproval) (string, evidence.ControllerEventStore, *evidence.Store, []evidence.Event) {
	t.Helper()
	target := filepath.Clean(t.TempDir())
	eventLog := evidence.ControllerEventStore{TargetDir: target}
	if err := eventLog.Reserve(runID); err != nil {
		t.Fatalf("reserve pinned human skip event log: %v", err)
	}
	store := workerHumanSkipEvidence(t, target, runID, eventLog)
	if value != nil {
		appendWorkerHumanApprovalEvents(t, store, *value)
	}
	events, err := eventLog.Read(runID)
	if err != nil {
		t.Fatalf("read pinned human skip run events: %v", err)
	}
	return target, eventLog, store, events
}

// TestValidateHumanInputSkipOfferBindsPinnedStagePolicy is the regression for
// the exact-SHA P2 finding: a skip offered or selected through a human-input
// approval must be rejected unless the run's immutable pinned workflow marks
// the stage skippable and the offer matches the pinned stage policy.
func TestValidateHumanInputSkipOfferBindsPinnedStagePolicy(t *testing.T) {
	const runID = "skip-offer-policy"
	skippableStages := []pinnedWorkerStage{
		{ID: "optional", Function: "product_owner", Result: "md", Skippable: true},
		{ID: "downstream", Function: "reviewer", Result: "md"},
	}
	nonSkippableStages := []pinnedWorkerStage{
		{ID: "optional", Function: "product_owner", Result: "md"},
		{ID: "downstream", Function: "reviewer", Result: "md"},
	}
	withPayload := func(mutate func(*approval.InputPayload)) func(*approval.PendingApproval) {
		return func(value *approval.PendingApproval) {
			var payload approval.InputPayload
			if err := json.Unmarshal(value.Payload, &payload); err != nil {
				t.Fatalf("decode base payload: %v", err)
			}
			mutate(&payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("encode mutated payload: %v", err)
			}
			value.Payload = encoded
		}
	}
	tests := []struct {
		name            string
		stages          []pinnedWorkerStage
		graph           workflow.Graph
		missingEvidence bool
		emptyTargetDir  bool
		mutate          func(*approval.PendingApproval)
		wantErr         string
	}{
		{name: "resolved skip on skippable stage", stages: skippableStages, graph: workerHumanSkipGraph(true)},
		{name: "pending offer on skippable stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate: func(value *approval.PendingApproval) {
				value.Status = approval.StatusPending
				value.ResolvedAction = ""
				value.ResolvedAt = time.Time{}
				value.Decisions = nil
			}},
		{name: "stage not skippable in pinned config", stages: nonSkippableStages, graph: workerHumanSkipGraph(true),
			wantErr: "not configured as skippable"},
		{name: "skippable stage without skipped route", stages: skippableStages, graph: workerHumanSkipGraph(false),
			wantErr: "no skipped route"},
		{name: "stage absent from pinned config", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate: func(value *approval.PendingApproval) {
				value.FromStage = "absent"
				value.ToStage = "absent"
				value.Targets = map[string]string{"reject": "absent", "submit": "absent", "skip": "absent"}
				withPayload(func(payload *approval.InputPayload) { payload.StageID = "absent" })(value)
			},
			wantErr: "not configured as skippable"},
		{name: "skip on non-input approval", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.Kind = approval.KindApprove },
			wantErr: "only allowed on a human input approval"},
		{name: "skip removed from actions", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.Actions = []string{"reject", "submit"} },
			wantErr: "not bound to its stage"},
		{name: "skip targets another stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.Targets["skip"] = "downstream" },
			wantErr: "not bound to its stage"},
		{name: "payload bound to another stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  withPayload(func(payload *approval.InputPayload) { payload.StageID = "downstream" }),
			wantErr: "payload does not match its stage"},
		{name: "action beyond pinned policy", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate: func(value *approval.PendingApproval) {
				value.Actions = append(value.Actions, "return_to_downstream")
				value.Targets["return_to_downstream"] = "downstream"
			},
			wantErr: "actions do not match"},
		{name: "decision role differs from pinned stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.RequiredRoles = []string{"reviewer"} },
			wantErr: "role policy"},
		{name: "quorum differs from pinned stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.Quorum = approval.QuorumAll },
			wantErr: "role policy"},
		{name: "payload result differs from pinned stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  withPayload(func(payload *approval.InputPayload) { payload.Result = "approve" }),
			wantErr: "output policy"},
		{name: "approve-result stage offers approve action", stages: []pinnedWorkerStage{
			{ID: "optional", Function: "product_owner", Result: "approve", Skippable: true},
			{ID: "downstream", Function: "reviewer", Result: "md"},
		}, graph: workerHumanSkipGraph(true),
			mutate: func(value *approval.PendingApproval) {
				value.Actions = []string{"reject", "approve", "skip"}
				value.Targets = map[string]string{"reject": "optional", "approve": "optional", "skip": "optional"}
				withPayload(func(payload *approval.InputPayload) { payload.Result = "approve" })(value)
			}},
		{name: "action target points at another stage", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate:  func(value *approval.PendingApproval) { value.Targets["reject"] = "downstream" },
			wantErr: "actions do not match"},
		{name: "resolved skip without a reason", stages: skippableStages, graph: workerHumanSkipGraph(true),
			mutate: func(value *approval.PendingApproval) {
				value.Decisions[len(value.Decisions)-1].Comment = "   "
			},
			wantErr: "requires an approved reason"},
		{name: "pinned evidence unavailable", emptyTargetDir: true,
			wantErr: "pinned workflow evidence is unavailable"},
		{name: "pinned evidence directory missing", missingEvidence: true,
			wantErr: "locate immutable run workflow"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Clean(t.TempDir())
			if !tc.missingEvidence && !tc.emptyTargetDir {
				eventLog := evidence.ControllerEventStore{TargetDir: target}
				if err := eventLog.Reserve(runID); err != nil {
					t.Fatalf("reserve pinned skip offer run: %v", err)
				}
				workerHumanSkipEvidenceForStages(t, target, runID, eventLog, tc.stages, tc.graph)
			}
			value := workerHumanSkipApproval(runID, "skip-offer")
			if tc.mutate != nil {
				tc.mutate(&value)
			}
			if tc.emptyTargetDir {
				target = ""
			}
			server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}}
			err := server.validateHumanInputSkipOffer(value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("pinned skippable skip offer rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("skip offer error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestPinnedWorkflowConfigRejectsTamperedRunEvidence keeps the skip-offer
// binding fail-closed: skip validation must only trust snapshots whose
// identity and digests still match the immutable run manifest.
func TestPinnedWorkflowConfigRejectsTamperedRunEvidence(t *testing.T) {
	const runID = "skip-offer-tamper"
	sha256Hex := func(data []byte) string {
		digest := sha256.Sum256(data)
		return hex.EncodeToString(digest[:])
	}
	patchManifest := func(t *testing.T, runDir string, mutate func(map[string]any)) {
		t.Helper()
		manifestPath := filepath.Join(runDir, "run.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read run manifest: %v", err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("decode run manifest: %v", err)
		}
		mutate(manifest)
		encoded, err := json.MarshalIndent(&manifest, "", "  ")
		if err != nil {
			t.Fatalf("encode run manifest: %v", err)
		}
		if err := os.Chmod(manifestPath, 0o644); err != nil {
			t.Fatalf("unlock run manifest: %v", err)
		}
		if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
			t.Fatalf("write run manifest: %v", err)
		}
	}
	rewriteSnapshot := func(t *testing.T, runDir, name string, data []byte) string {
		t.Helper()
		path := filepath.Join(runDir, name)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("unlock snapshot %s: %v", name, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write snapshot %s: %v", name, err)
		}
		return sha256Hex(data)
	}
	graphWithoutReachableNodes := func() workflow.Graph {
		graph := workerHumanSkipGraph(true)
		graph.Nodes = append(graph.Nodes, workflow.Node{ID: "orphan"})
		return graph
	}
	workflowSnapshot := func(graph workflow.Graph) []byte {
		data, err := json.Marshal(struct {
			SchemaVersion int            `json:"schema_version"`
			Graph         workflow.Graph `json:"graph"`
			Stages        []any          `json:"stages"`
		}{SchemaVersion: 2, Graph: graph, Stages: []any{}})
		if err != nil {
			t.Fatalf("encode workflow snapshot: %v", err)
		}
		return data
	}
	tests := []struct {
		name    string
		mutate  func(t *testing.T, runDir string)
		wantErr string
	}{
		{name: "manifest run identity", wantErr: "identity is invalid",
			mutate: func(t *testing.T, runDir string) {
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["run_id"] = "another-run" })
			}},
		{name: "missing run manifest", wantErr: "read immutable run manifest",
			mutate: func(t *testing.T, runDir string) {
				if err := os.Remove(filepath.Join(runDir, "run.json")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "missing config snapshot", wantErr: "verify immutable run config",
			mutate: func(t *testing.T, runDir string) {
				if err := os.Remove(filepath.Join(runDir, "config.json")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "missing workflow snapshot", wantErr: "verify immutable run workflow",
			mutate: func(t *testing.T, runDir string) {
				if err := os.Remove(filepath.Join(runDir, "workflow.json")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "config digest mismatch", wantErr: "digest mismatch",
			mutate: func(t *testing.T, runDir string) {
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["config_sha256"] = strings.Repeat("0", 64) })
			}},
		{name: "config snapshot path traversal", wantErr: "snapshot path is invalid",
			mutate: func(t *testing.T, runDir string) {
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["config_evidence"] = "../run.json" })
			}},
		{name: "undecodable config snapshot", wantErr: "decode immutable run config",
			mutate: func(t *testing.T, runDir string) {
				digest := rewriteSnapshot(t, runDir, "config.json", []byte("{"))
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["config_sha256"] = digest })
			}},
		{name: "config snapshot without stages", wantErr: "no template stages",
			mutate: func(t *testing.T, runDir string) {
				data, err := json.Marshal(pinnedWorkerConfig{SchemaVersion: 5})
				if err != nil {
					t.Fatal(err)
				}
				digest := rewriteSnapshot(t, runDir, "config.json", data)
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["config_sha256"] = digest })
			}},
		{name: "undecodable workflow snapshot", wantErr: "decode immutable run workflow",
			mutate: func(t *testing.T, runDir string) {
				digest := rewriteSnapshot(t, runDir, "workflow.json", []byte("{"))
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["resolved_workflow_sha256"] = digest })
			}},
		{name: "workflow snapshot without a graph", wantErr: "no compiled graph",
			mutate: func(t *testing.T, runDir string) {
				digest := rewriteSnapshot(t, runDir, "workflow.json", []byte(`{"schema_version":2}`))
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["resolved_workflow_sha256"] = digest })
			}},
		{name: "structurally invalid pinned graph", wantErr: "invalid immutable run graph",
			mutate: func(t *testing.T, runDir string) {
				digest := rewriteSnapshot(t, runDir, "workflow.json", workflowSnapshot(graphWithoutReachableNodes()))
				patchManifest(t, runDir, func(manifest map[string]any) { manifest["resolved_workflow_sha256"] = digest })
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Clean(t.TempDir())
			eventLog := evidence.ControllerEventStore{TargetDir: target}
			if err := eventLog.Reserve(runID); err != nil {
				t.Fatalf("reserve tampered run: %v", err)
			}
			workerHumanSkipEvidenceForStages(t, target, runID, eventLog,
				[]pinnedWorkerStage{{ID: "optional", Function: "product_owner", Result: "md", Skippable: true}},
				workerHumanSkipGraph(true))
			tc.mutate(t, filepath.Join(target, ".ai-team", "runs", runID))
			server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}}
			err := server.validateHumanInputSkipOffer(workerHumanSkipApproval(runID, "skip-offer"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("skip offer against tampered evidence error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestControllerHumanInputAuthorizationRejectsMismatchedAuthority(t *testing.T) {
	base := workerHumanSkipApproval("human-auth-run", "human-auth-approval")
	tests := []struct {
		name      string
		store     func(approval.PendingApproval) *apiApprovalStore
		approval  func(*approval.PendingApproval)
		events    func([]evidence.Event) []evidence.Event
		stage     string
		id        string
		wantValid bool
	}{
		{name: "valid", wantValid: true},
		{name: "missing store", store: func(approval.PendingApproval) *apiApprovalStore { return nil }},
		{name: "missing id", id: ""},
		{name: "store load error", store: func(v approval.PendingApproval) *apiApprovalStore {
			return &apiApprovalStore{values: map[string]approval.PendingApproval{}, loadErr: errors.New("unavailable")}
		}},
		{name: "wrong run", approval: func(v *approval.PendingApproval) { v.RunID = "another-run" }},
		{name: "wrong id", approval: func(v *approval.PendingApproval) { v.ID = "other-approval" }},
		{name: "wrong kind", approval: func(v *approval.PendingApproval) { v.Kind = approval.KindApprove }},
		{name: "wrong trigger", approval: func(v *approval.PendingApproval) { v.Trigger = "graph_outcome:blocked" }},
		{name: "pending", approval: func(v *approval.PendingApproval) { v.Status = approval.StatusPending }},
		{name: "wrong from stage", approval: func(v *approval.PendingApproval) { v.FromStage = "other" }},
		{name: "wrong to stage", approval: func(v *approval.PendingApproval) { v.ToStage = "other" }},
		{name: "missing attempt", approval: func(v *approval.PendingApproval) { v.AttemptID = "" }},
		{name: "missing subject", approval: func(v *approval.PendingApproval) { v.SubjectHash = "" }},
		{name: "missing resolved time", approval: func(v *approval.PendingApproval) { v.ResolvedAt = time.Time{} }},
		{name: "missing action", approval: func(v *approval.PendingApproval) { v.ResolvedAction = "" }},
		{name: "action not listed", approval: func(v *approval.PendingApproval) { v.Actions = []string{"submit"} }},
		{name: "wrong target", approval: func(v *approval.PendingApproval) { v.Targets["skip"] = "other" }},
		{name: "no decision", approval: func(v *approval.PendingApproval) { v.Decisions = nil }},
		{name: "invalid payload", approval: func(v *approval.PendingApproval) { v.Payload = json.RawMessage(`{`) }},
		{name: "wrong payload kind", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"approve","stage_id":"optional","output_name":"out","output_path":"out.md"}`)
		}},
		{name: "wrong payload stage", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"elsewhere","output_name":"out","output_path":"out.md"}`)
		}},
		{name: "empty output name", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"optional","output_path":"out.md"}`)
		}},
		{name: "empty output path", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"optional","output_name":"out"}`)
		}},
		{name: "decision approval mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].ApprovalID = "other" }},
		{name: "decision subject mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].SubjectHash = strings.Repeat("e", 64) }},
		{name: "decision action mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].Action = "submit" }},
		{name: "decision actor missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].ActorID = " " }},
		{name: "decision role missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].ActorRole = " " }},
		{name: "decision time missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].DecidedAt = time.Time{} }},
		{name: "skip reason missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].Comment = "  " }},
		{name: "decision role unauthorized", approval: func(v *approval.PendingApproval) { v.RequiredRoles = []string{"reviewer"} }},
		{name: "no decision event", events: func([]evidence.Event) []evidence.Event { return nil }},
		{name: "wrong event attempt", events: func(events []evidence.Event) []evidence.Event { events[0].AttemptID = "other-attempt"; return events }},
		{name: "wrong event identity", events: func(events []evidence.Event) []evidence.Event { events[0].Data["trigger"] = "other"; return events }},
		{name: "wrong event decisions", events: func(events []evidence.Event) []evidence.Event { events[0].Data["decisions"] = []any{}; return events }},
		{name: "wrong stage", stage: "other"},
		{name: "wrong lookup id", id: "other-approval"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := workerHumanSkipApproval(base.RunID, base.ID)
			value.Decisions = append([]approval.Decision(nil), value.Decisions...)
			value.Actions = append([]string(nil), value.Actions...)
			value.RequiredRoles = append([]string(nil), value.RequiredRoles...)
			value.Targets = map[string]string{"reject": "optional", "submit": "optional", "skip": "optional"}
			if tc.approval != nil {
				tc.approval(&value)
			}
			store := &apiApprovalStore{values: map[string]approval.PendingApproval{value.RunID + "/" + base.ID: value}}
			if tc.store != nil {
				store = tc.store(value)
			}
			if tc.name == "missing store" {
				store = nil
			}
			events := workerHumanAuthorizationEvents(value)
			targetDir := filepath.Clean(t.TempDir())
			// Every case runs against real pinned evidence so a rejection can
			// never be explained by a missing TargetDir alone.
			controllerEvents := evidence.ControllerEventStore{TargetDir: targetDir}
			if err := controllerEvents.Reserve(base.RunID); err != nil {
				t.Fatal(err)
			}
			runStore := workerHumanSkipEvidence(t, targetDir, base.RunID, controllerEvents)
			var eventLog evidence.EventLog
			var err error
			if tc.wantValid {
				appendWorkerHumanApprovalEvents(t, runStore, value)
				events, err = controllerEvents.Read(base.RunID)
				if err != nil {
					t.Fatal(err)
				}
				eventLog = controllerEvents
			}
			if tc.events != nil {
				events = tc.events(events)
			}
			stage, id := "optional", base.ID
			if tc.stage != "" {
				stage = tc.stage
			}
			if tc.id != "" || tc.name == "missing id" {
				id = tc.id
			}
			var authority workerApprovalPort
			if store != nil {
				authority = store
			}
			server := &workerAPIServer{scope: workerAPIScope{RunID: base.RunID, TargetDir: targetDir}, approvals: authority, eventLogs: eventLog}
			_, _, err = server.authorizedHumanInput(events, stage, id)
			if tc.wantValid && err != nil {
				t.Fatalf("valid controller approval rejected: %v", err)
			}
			if !tc.wantValid && err == nil {
				t.Fatal("mismatched human input authority was accepted")
			}
		})
	}
}

func TestControllerHumanSkipRecoveryHasNoAgentAuthorityAndIgnoresCompletedRecords(t *testing.T) {
	server := &workerAPIServer{scope: workerAPIScope{RunID: "human-skip-scan", TargetDir: "/tmp/human-skip-scan"}}
	start := evidence.Event{Type: "attempt_started", Stage: "agent", AttemptID: "agent-skip", Data: map[string]any{
		"executor": "agent", "stage_action": "skip",
	}}
	finish := evidence.Event{Type: "attempt_finished", Stage: "agent", AttemptID: "agent-skip", Data: map[string]any{
		"executor": "agent", "outcome": "skipped",
	}}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start, finish}); err == nil || !strings.Contains(err.Error(), "no authority to repair agent skip") {
		t.Fatalf("worker-backed controller accepted agent skip recovery without authority: %v", err)
	}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start}); err != nil {
		t.Fatalf("unfinished attempt should not trigger skip recovery: %v", err)
	}
	warning := evidence.Event{Type: "stage_skipped", Stage: "agent", AttemptID: "agent-skip"}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start, finish, warning}); err != nil {
		t.Fatalf("existing warning should not require a recovery authority lookup: %v", err)
	}

	humanTarget := t.TempDir()
	humanEventStore := evidence.ControllerEventStore{TargetDir: humanTarget}
	if err := humanEventStore.Reserve("human-skip-scan"); err != nil {
		t.Fatal(err)
	}
	humanRunStore := workerHumanSkipEvidence(t, humanTarget, "human-skip-scan", humanEventStore)
	value := workerHumanSkipApproval("human-skip-scan", "recovery-approval")
	appendWorkerHumanApprovalEvents(t, humanRunStore, value)
	humanServer := &workerAPIServer{scope: workerAPIScope{RunID: "human-skip-scan", TargetDir: humanTarget},
		approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{value.RunID + "/" + value.ID: value}}, eventLogs: humanEventStore}
	humanStart := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "human-skip", Timestamp: time.Now().UTC(), Data: map[string]any{
		"executor": "human", "stage_action": "skip", "stage_skip_version": float64(evidence.StageSkipProtocolVersion),
		"stage_index": float64(1), "actor_id": "alice", "actor_role": "product_owner", "human_input_approval_id": "missing",
	}}
	humanFinish := evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: "human-skip", Timestamp: humanStart.Timestamp.Add(time.Second), Data: map[string]any{
		"executor": "human", "outcome": "skipped", "actor_id": "alice", "actor_role": "product_owner",
		"human_input_approval_id": "missing", "stage_skip_reason": "No document is needed.",
	}}
	if err := humanServer.validateMissingHumanSkipAuthorities([]evidence.Event{humanStart, humanFinish}); err == nil || !strings.Contains(err.Error(), "authorize human skip recovery") {
		t.Fatalf("recovery accepted a human skip without approval authority: %v", err)
	}
	humanStart.Data["human_input_approval_id"] = value.ID
	humanFinish.Data["human_input_approval_id"] = value.ID
	humanServer.approvals = &apiApprovalStore{values: map[string]approval.PendingApproval{value.RunID + "/" + value.ID: value}}
	approvalEvents, err := humanEventStore.Read("human-skip-scan")
	if err != nil {
		t.Fatal(err)
	}
	events := append(approvalEvents, humanStart, humanFinish)
	if err := humanServer.validateMissingHumanSkipAuthorities(events); err == nil || !strings.Contains(err.Error(), "approval-matched empty-output manifest") {
		t.Fatalf("recovery accepted a human skip without its controller manifest: %v", err)
	}
}

func TestControllerHumanAppendHelpersRejectMalformedAndRetrySafely(t *testing.T) {
	server := &workerAPIServer{scope: workerAPIScope{RunID: "human-helper-run"}}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": "id"}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": "id", "actor_id": "worker"}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": 9}},
	} {
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil {
			t.Fatalf("malformed human start was accepted: %+v", request)
		}
	}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt"},
		{Type: "attempt_finished", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"outcome": "skipped"}},
	} {
		if _, err := server.appendControllerHumanAttemptFinished(request); err == nil {
			t.Fatalf("malformed human finish was accepted: %+v", request)
		}
	}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt"},
		{Type: "stage_skipped", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"reason": "worker"}},
	} {
		if _, err := server.appendControllerHumanStageSkip(request, nil); err == nil {
			t.Fatalf("malformed stage skip was accepted: %+v", request)
		}
	}

	stored := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Timestamp: time.Unix(10, 0).UTC(),
		Data: map[string]any{"executor": "human", "stage_index": float64(1)}}
	server.eventLogs = nil
	if got, err := server.appendControllerHumanEvent([]evidence.Event{stored}, stored, false); err != nil || got.AttemptID != stored.AttemptID {
		t.Fatalf("exact controller event retry was not idempotent: event=%+v err=%v", got, err)
	}
	conflict := stored
	conflict.Data = map[string]any{"executor": "agent", "stage_index": float64(1)}
	if _, err := server.appendControllerHumanEvent([]evidence.Event{stored}, conflict, false); err == nil {
		t.Fatal("conflicting retry was accepted")
	}
	if _, err := server.appendControllerHumanEvent(nil, stored, false); err == nil {
		t.Fatal("controller append without an initialized run chain was accepted")
	}
	if _, err := server.appendControllerHumanEvent(nil, stored, true); err == nil {
		t.Fatal("stage skip append without an initialized run chain was accepted")
	}
	if controllerEventDataEqual(map[string]any{"unsupported": make(chan int)}, map[string]any{}) {
		t.Fatal("event data with non-serializable authority was treated as an idempotent retry")
	}
	badCandidate := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "bad-attempt", Timestamp: time.Now().UTC(), Data: map[string]any{
		"executor": "human", "stage_index": "not-a-number", "actor_id": "alice", "actor_role": "product_owner", "human_input_approval_id": "approval",
	}}
	chain := []evidence.Event{{Type: "run_started", RunID: server.scope.RunID, Sequence: 1, Timestamp: time.Now().UTC()}}
	server.scope.TargetDir = t.TempDir()
	server.eventLogs = &validationEventLog{events: chain}
	if _, err := server.appendControllerHumanEvent(chain, badCandidate, false); err == nil {
		t.Fatal("controller accepted an invalid human attempt event")
	}
	if event, ok := controllerEventForAttempt([]evidence.Event{stored}, stored.Type, stored.Stage, stored.AttemptID); !ok || event.AttemptID != stored.AttemptID {
		t.Fatal("controller event lookup did not find matching identity")
	}
	if _, ok := controllerEventForAttempt(nil, stored.Type, stored.Stage, stored.AttemptID); ok {
		t.Fatal("controller event lookup matched an absent event")
	}
	for _, tc := range []struct {
		value any
		want  int
	}{{1, 1}, {float64(2), 2}, {float64(1.5), 0}, {"1", 0}, {nil, 0}} {
		if got := numericWorkerVersion(tc.value); got != tc.want {
			t.Fatalf("numericWorkerVersion(%v)=%d want %d", tc.value, got, tc.want)
		}
	}
}

func TestControllerHumanDispatchEnforcesTransportAndLifecycleBoundaries(t *testing.T) {
	const runID = "human-dispatch-boundaries"
	request := workerAPICall{RunID: runID, Event: evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: "attempt"}}
	for _, tc := range []struct {
		name   string
		method string
		server *workerAPIServer
		call   workerAPICall
		want   string
	}{
		{name: "start requires trusted transport", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "start run identity", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "run mismatch"},
		{name: "start operation", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "finish requires trusted transport", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "finish run identity", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "run mismatch"},
		{name: "finish operation", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "skip requires trusted transport", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "skip request identity", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "only run, stage, and attempt identity"},
		{name: "skip operation", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "skip journal read", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationResume}, usageAllowed: true,
			eventLogs: &validationEventLog{readErr: errors.New("journal read failed")}}, call: request, want: "read event chain before controller stage skip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.server.dispatch(tc.method, tc.call)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("dispatch error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestControllerHumanFinishAndSkipRequireTheirOwnEvidence(t *testing.T) {
	const runID, attemptID, approvalID = "human-finish-evidence", "attempt-evidence", "approval-evidence"
	value := workerHumanSkipApproval(runID, approvalID)
	started := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID,
		Timestamp: time.Now().UTC(), Data: map[string]any{"executor": "human", "stage_index": float64(1), "human_input_approval_id": approvalID,
			"stage_action": "skip", "stage_skip_version": float64(evidence.StageSkipProtocolVersion), "actor_id": "alice", "actor_role": "product_owner"}}

	t.Run("finish requires event chain", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{}); err == nil || !strings.Contains(err.Error(), "invalid identity or fields") {
			t.Fatalf("malformed finish reached event chain: %v", err)
		}
	})
	t.Run("finish requires controller human start", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "no controller-owned start") {
			t.Fatalf("finish without a controller human start: %v", err)
		}
	})
	t.Run("finish fails closed when its journal cannot be read", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{readErr: errors.New("controller journal unavailable")}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "controller journal unavailable") {
			t.Fatalf("finish ignored a journal read failure: %v", err)
		}
	})
	t.Run("finish requires matching approval", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{events: []evidence.Event{started}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "approval authority is unavailable") {
			t.Fatalf("finish without an approval authority: %v", err)
		}
	})
	t.Run("finish requires controller manifest", func(t *testing.T) {
		target, _, _, authorized := workerHumanSkipPinnedRun(t, runID, &value)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "read controller human attempt manifest") {
			t.Fatalf("finish without a controller manifest: %v", err)
		}
	})
	t.Run("finish rejects a manifest for another stage", func(t *testing.T) {
		target, _, _, authorized := workerHumanSkipPinnedRun(t, runID, &value)
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, started.Timestamp.Add(time.Second), approvalID, "alice", "product_owner")
		manifest.Stage = "elsewhere"
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "manifest does not match") {
			t.Fatalf("finish accepted a manifest for another stage: %v", err)
		}
	})
	t.Run("skip decision requires a skipped manifest", func(t *testing.T) {
		target, _, _, authorized := workerHumanSkipPinnedRun(t, runID, &value)
		finishedAt := started.Timestamp.Add(time.Second)
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, finishedAt, approvalID, "alice", "product_owner")
		manifest.Outcome, manifest.Status, manifest.Decision = string(workflow.OutcomePassed), string(workflow.OutcomePassed), string(workflow.DecisionApproved)
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "skip manifest") {
			t.Fatalf("skip approval accepted a passed manifest: %v", err)
		}
	})
	t.Run("submit decision cannot finish as skipped", func(t *testing.T) {
		target := t.TempDir()
		submit := workerHumanSubmitApproval(runID, approvalID)
		finishedAt := started.Timestamp.Add(time.Second)
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, finishedAt, approvalID, "alice", "product_owner")
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		events := append(workerHumanAuthorizationEvents(submit), started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: events},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: submit}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "cannot be skipped") {
			t.Fatalf("submit approval accepted a skipped manifest: %v", err)
		}
	})
	t.Run("skip warning requires human start", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, nil); err == nil || !strings.Contains(err.Error(), "requires a human attempt") {
			t.Fatalf("warning without a human start: %v", err)
		}
	})
	t.Run("skip warning requires approved skip action", func(t *testing.T) {
		submit := workerHumanSubmitApproval(runID, approvalID)
		_, _, _, events := workerHumanSkipPinnedRun(t, runID, &submit)
		events = append(events, started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: submit}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, events); err == nil || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("submit approval authorized a skip warning: %v", err)
		}
	})
	t.Run("skip warning requires matching finished attempt", func(t *testing.T) {
		target, _, _, authorized := workerHumanSkipPinnedRun(t, runID, &value)
		events := append(authorized, started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, events); err == nil || !strings.Contains(err.Error(), "no matching approved finished attempt") {
			t.Fatalf("warning without an approved skipped finish: %v", err)
		}
	})
	t.Run("skip warning requires durable approval", func(t *testing.T) {
		unauthorized := started
		unauthorized.Data = map[string]any{"executor": "human", "stage_index": float64(1), "human_input_approval_id": "missing-approval"}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, []evidence.Event{unauthorized}); err == nil || !strings.Contains(err.Error(), "approval") {
			t.Fatalf("warning without a durable input approval: %v", err)
		}
	})
}

func TestControllerHumanStartReadsAuthorityBeforeCreatingAttempt(t *testing.T) {
	const runID, approvalID = "human-start-boundary", "approval-start-boundary"
	value := workerHumanSkipApproval(runID, approvalID)
	request := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "attempt-start-boundary",
		Data: map[string]any{"stage_index": float64(1), "human_input_approval_id": approvalID}}
	authority := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
	t.Run("rejects extra actor authority fields", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		forged := request
		forged.Data = map[string]any{"stage_index": float64(1), "actor_id": "worker"}
		if _, err := server.appendControllerHumanAttemptStarted(forged); err == nil || !strings.Contains(err.Error(), "unauthorized field") {
			t.Fatalf("worker actor field was accepted: %v", err)
		}
	})
	t.Run("rejects journal read failures", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{readErr: errors.New("journal unavailable")}}
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil || !strings.Contains(err.Error(), "journal unavailable") {
			t.Fatalf("human start ignored journal read failure: %v", err)
		}
	})
	t.Run("rejects a start without a matching durable approval event", func(t *testing.T) {
		target, _, _, events := workerHumanSkipPinnedRun(t, runID, nil)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: events}, approvals: authority}
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil || !strings.Contains(err.Error(), "matching durable approval_decided event") {
			t.Fatalf("human start without a durable approval event was accepted: %v", err)
		}
	})
	t.Run("zero timestamp is assigned only after authority is verified", func(t *testing.T) {
		target, _, _, events := workerHumanSkipPinnedRun(t, runID, &value)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: events}, approvals: authority}
		if _, err := server.appendControllerHumanAttemptStarted(request); err != nil && strings.Contains(err.Error(), "predates") {
			t.Fatalf("server timestamp was checked before it was assigned: %v", err)
		}
	})
}

func workerHumanAuthorizationEvents(value approval.PendingApproval) []evidence.Event {
	return []evidence.Event{{Type: "approval_decided", AttemptID: value.AttemptID, Data: map[string]any{
		"approval_id": value.ID, "kind": string(value.Kind), "subject_hash": value.SubjectHash,
		"status": string(value.Status), "resolved_action": value.ResolvedAction,
		"from_stage": value.FromStage, "to_stage": value.ToStage, "trigger": value.Trigger,
		"decisions": value.Decisions,
	}}}
}

type failingHumanSkipAppendLog struct {
	evidence.EventLog
	appendErr error
}

func (l failingHumanSkipAppendLog) Append(string, evidence.Event, uint64, string) (evidence.Event, error) {
	return evidence.Event{}, l.appendErr
}
