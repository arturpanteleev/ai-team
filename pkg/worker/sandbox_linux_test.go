//go:build linux

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

const (
	sandboxBriefAncestorProbeApprovalID = "ancestor-redirection-probe"
	sandboxBriefAncestorProbeQuestions  = "where does the controller write?"
	sandboxBriefAncestorProbeAnswer     = "to its pinned directory"
)

func TestProcessEngineStartReservesEventAuthorityBeforeCandidateAdmission(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("Linux candidate-admission ordering test requires bubblewrap")
	}
	for _, tc := range []struct {
		name string
		git  bool
	}{{name: "non-git"}, {name: "git", git: true}} {
		t.Run(tc.name, func(t *testing.T) {
			target := makeBubblewrapTarget(t)
			if tc.git {
				if output, err := exec.Command("git", "init", target).CombinedOutput(); err != nil {
					t.Fatalf("initialize Git target: %v\n%s", err, output)
				}
			}
			runID := "candidate-admission-order-" + tc.name
			engine, err := NewProcessEngine(
				[]string{os.Args[0]}, target, filepath.Join(target, "missing-db-parent", "controller.db"),
				WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
			)
			if err != nil {
				t.Fatal(err)
			}
			engine.bubblewrap = true
			job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: runID, TargetDir: target, Feature: "feature", Task: "task"}
			if _, err := engine.Execute(context.Background(), job); err == nil || !strings.Contains(err.Error(), "controller database path") {
				t.Fatalf("expected bubblewrap builder to stop before worker spawn after admission setup, got %v", err)
			}
			if reserved, err := (evidence.ControllerEventStore{TargetDir: target}).IsReserved(runID); err != nil || !reserved {
				t.Fatalf("event authority must be reserved before candidate admission: reserved=%t err=%v", reserved, err)
			}
			if tc.git {
				if err := (candidate.FileMetadataStore{}).ReadGitAdmission(target, runID); err != nil {
					t.Fatalf("Git candidate admission proof was not written after event reservation: %v", err)
				}
			} else if err := (candidate.FileMetadataStore{}).ReadAbsent(target, runID); err != nil {
				t.Fatalf("non-Git candidate admission proof was not written after event reservation: %v", err)
			}
		})
	}
}

func TestProcessEngineStartRejectsConflictingCandidateAdmissionAfterReservation(t *testing.T) {
	target := makeBubblewrapTarget(t)
	const runID = "candidate-admission-conflict"
	events := evidence.ControllerEventStore{TargetDir: target}
	if err := events.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if err := (candidate.FileMetadataStore{}).MarkAbsent(target, runID); err != nil {
		t.Fatal(err)
	}
	engine, err := NewProcessEngine(
		[]string{os.Args[0]}, target, filepath.Join(target, "controller.db"),
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	engine.bubblewrap = true
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: runID, TargetDir: target, Feature: "feature", Task: "task"}
	if _, err := engine.Execute(context.Background(), job); err == nil || !strings.Contains(err.Error(), "worker candidate admission: non-Git Start refuses a pre-existing candidate absence marker") {
		t.Fatalf("conflicting candidate admission must fail after event authority is established: %v", err)
	}
	if reserved, err := events.IsReserved(runID); err != nil || !reserved {
		t.Fatalf("event authority reservation was lost after rejected admission: reserved=%t err=%v", reserved, err)
	}
}

func TestProcessEngineAPISocketFailureDoesNotLeaveCandidateAdmissionProof(t *testing.T) {
	target := makeBubblewrapTarget(t)
	const runID = "candidate-admission-socket-failure"
	engine, err := NewProcessEngine(
		[]string{os.Args[0]}, target, filepath.Join(target, "controller.db"),
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	engine.bubblewrap = true
	engine.controlSocketDirCreator = func(target, _, _ string) (string, error) {
		dir := filepath.Join(target, ".ai-team", "controller", "occupied-socket")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "controller-api.sock"), []byte("occupied"), 0600); err != nil {
			return "", err
		}
		return dir, nil
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: runID, TargetDir: target, Feature: "feature", Task: "task"}
	if _, err := engine.Execute(context.Background(), job); err == nil || !strings.Contains(err.Error(), "worker controller API") || !strings.Contains(err.Error(), "listen on worker controller API socket") {
		t.Fatalf("expected controller API socket bind failure, got %v", err)
	}
	if admitted, err := (candidate.FileMetadataStore{}).HasControllerAdmissionProof(target, runID); err != nil || admitted {
		t.Fatalf("failed controller setup must not leave candidate admission proof: admitted=%t err=%v", admitted, err)
	}
	if reserved, err := (evidence.ControllerEventStore{TargetDir: target}).IsReserved(runID); err != nil || reserved {
		t.Fatalf("event authority should remain absent when API setup fails: reserved=%t err=%v", reserved, err)
	}
}

func TestBubblewrapWorkerCannotReadControllerStateAndCanUseTarget(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("Linux CI must install bubblewrap before running worker sandbox tests:", err)
	}
	target := t.TempDir()
	// Match cloud Start ordering: reserve controller event authority before
	// publishing any run-scoped controller markers or spawning the worker.
	if err := (evidence.ControllerEventStore{TargetDir: target}).Reserve("sandbox-probe"); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(target, ".ai-team", "controller")
	if err := os.MkdirAll(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(controlDir, "controller.db")
	dbSecret := []byte("controller-db-secret")
	if err := os.WriteFile(dbPath, dbSecret, 0600); err != nil {
		t.Fatal(err)
	}
	sidecarSecrets := map[string]string{
		"--probe-wal":     "controller-wal-secret",
		"--probe-shm":     "controller-shm-secret",
		"--probe-journal": "controller-journal-secret",
	}
	for flag, secret := range sidecarSecrets {
		suffix := strings.TrimPrefix(strings.TrimPrefix(flag, "--probe"), "-")
		path := dbPath + "-" + suffix
		if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleDir := filepath.Join(target, ".ai-team", "state", "runs")
	if err := os.MkdirAll(lifecycleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lifecycleDir, "controller-state.json"), []byte("lifecycle-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	eventAuthorityDir := filepath.Join(lifecycleDir, "event-log-authority")
	if err := os.MkdirAll(eventAuthorityDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventAuthorityDir, "controller-sentinel.json"), []byte("event-authority-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	approvalDir := filepath.Join(target, ".ai-team", "state", "approvals")
	if err := os.MkdirAll(approvalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approvalDir, "pending.json"), []byte("legacy-approval-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	candidateMetadataDir := filepath.Join(target, ".ai-team", "state", "candidates")
	if err := os.MkdirAll(candidateMetadataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateMetadataDir, "controller-sentinel.json"), []byte("candidate-metadata-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	candidateEvidenceRoot := filepath.Join(target, ".ai-team", "state", "evidence")
	probeCandidateEvidence := pipeline.CandidateEvidence{
		SchemaVersion: 1, RunID: "sandbox-probe", Purpose: "semantic_code_review",
		WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: []pipeline.CandidateFile{},
		Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
	}
	if err := writeControllerCandidateEvidence(candidateEvidenceRoot, probeCandidateEvidence.RunID, "review-candidate.json", probeCandidateEvidence); err != nil {
		t.Fatal(err)
	}
	usageDir := filepath.Join(target, ".ai-team", "state", "usage")
	if err := os.MkdirAll(usageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usageDir, "controller-sentinel.json"), []byte("usage-envelope-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	deliveryRecord := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: "sandbox-probe", Feature: "probe", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	if err := delivery.WriteControllerTerminalRecord(target, deliveryRecord.RunID, deliveryRecord); err != nil {
		t.Fatal(err)
	}
	if err := delivery.WriteControllerDeliveryReceipt(target, deliveryRecord); err != nil {
		t.Fatal(err)
	}
	// Model an interruption after the Git effect but before the controller
	// receipt was persisted. The worker must not be able to create it; the
	// parent reconciler must still be able to issue and read it after exit.
	if err := os.Remove(filepath.Join(target, ".ai-team", "state", "delivery-receipts", deliveryRecord.RunID+".json")); err != nil {
		t.Fatal(err)
	}
	manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := manifestStore.Reserve("sandbox-probe"); err != nil {
		t.Fatal(err)
	}
	manifestNow := time.Now().UTC()
	manifestSentinel := evidence.AttemptManifest{SchemaVersion: evidence.SchemaVersion, RunID: "sandbox-probe", AttemptID: "controller-sentinel", Stage: "probe", StageIndex: 1,
		StartedAt: manifestNow.Add(-time.Minute), FinishedAt: manifestNow, Status: "completed", Execution: "success", Decision: "continue", Outcome: "success"}
	if err := manifestStore.Write("sandbox-probe", manifestSentinel); err != nil {
		t.Fatal(err)
	}
	seedAttestation := &attest.Statement{Type: attest.StatementType, PredicateType: attest.PredicateTypeV1, Predicate: attest.Predicate{SchemaVersion: attest.PredicateSchemaVersion, RunID: "sandbox-probe"}}
	seedAttestationBytes, _ := json.Marshal(seedAttestation)
	if err := (attest.ControllerStore{TargetDir: target}).Write("sandbox-probe", seedAttestationBytes); err != nil {
		t.Fatal(err)
	}
	seedContainment := containment.DefaultTrustedLocalReceipt()
	if err := (containment.ControllerReceiptStore{TargetDir: target}).Write("sandbox-probe", seedContainment); err != nil {
		t.Fatal(err)
	}
	briefStore := pipeline.NewControllerBriefStore(target)
	t.Cleanup(func() { _ = briefStore.Close() })
	if err := briefStore.PrepareRun("sandbox-probe"); err != nil {
		t.Fatal(err)
	}
	brief, err := briefStore.CreateInitial("sandbox-probe", "test worker filesystem boundary")
	if err != nil {
		t.Fatal(err)
	}
	otherRunBriefStore := pipeline.NewControllerBriefStore(target)
	t.Cleanup(func() { _ = otherRunBriefStore.Close() })
	if err := otherRunBriefStore.PrepareRun("sandbox-other-run"); err != nil {
		t.Fatal(err)
	}
	otherRunBrief, err := otherRunBriefStore.CreateInitial("sandbox-other-run", "other run sentinel")
	if err != nil {
		t.Fatal(err)
	}
	otherRunBriefPath := filepath.Join(target, ".ai-team", "state", "briefs", "sandbox-other-run", filepath.Base(otherRunBrief.Version.Path))
	otherRunBriefBytes, err := os.ReadFile(otherRunBriefPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBriefRoot := filepath.Join(target, ".ai-team", "state", "briefs")
	canonicalBriefRootBefore, err := os.Stat(canonicalBriefRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", "sandbox-probe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh run evidence directory exists before worker start: err=%v", err)
	}
	candidateWorktree := filepath.Join(target, ".ai-team", "worktrees", "probe")
	if err := os.MkdirAll(candidateWorktree, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateWorktree, "visible.txt"), []byte("worktree-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "visible.txt"), []byte("target-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()
	agentDir := filepath.Join(target, ".ai-team", "agents")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "role.md")
	if err := os.WriteFile(agentPath, []byte("agent-definition"), 0600); err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(target, "sandbox-probe.json")
	if err := (candidate.FileMetadataStore{}).MarkAbsent(target, "sandbox-probe"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_TEAM_BUBBLEWRAP_PROBE", "1")
	// These sentinels are deliberately present in the controller's parent
	// environment. The runtime probe verifies that none crosses the worker
	// environment boundary. OPENAI_API_KEY is provider-scoped and is not part
	// of this control-plane secret assertion.
	t.Setenv("AI_TEAM_AUTH_SECRET", "probe-auth-control-secret")
	t.Setenv("AI_TEAM_SIGNING_KEY", "probe-signing-control-secret")
	t.Setenv("AI_TEAM_DB_PASSWORD", "probe-db-control-secret")
	t.Setenv("AI_TEAM_HOSTING_WRITE_TOKEN", "probe-hosting-control-secret")
	allowWorkerTestEnvironment(t, "AI_TEAM_BUBBLEWRAP_PROBE")
	probeApproval := workerQuestionApproval("sandbox-probe", sandboxBriefAncestorProbeApprovalID, sandboxBriefAncestorProbeQuestions, sandboxBriefAncestorProbeAnswer, approval.StatusResolved)
	var hostRecoveryCalled bool
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestBubblewrapWorkerProbeHelper$", "--", "--probe-db", dbPath,
			"--probe-wal", dbPath + "-wal", "--probe-shm", dbPath + "-shm", "--probe-journal", dbPath + "-journal",
			"--probe-host-tcp", hostListener.Addr().String(), "--probe-output", probePath},
		target, dbPath,
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{"sandbox-probe/" + sandboxBriefAncestorProbeApprovalID: probeApproval}}),
		WithAgentRegistryPaths([]string{agentDir}),
		WithLinuxBubblewrapIsolation(),
		WithTerminalDeliveryReconciler(func(_ context.Context, runID, targetDir string) error {
			if runID != "sandbox-probe" || targetDir != target {
				return fmt.Errorf("host recovery scope mismatch: run=%q target=%q", runID, targetDir)
			}
			data, readErr := os.ReadFile(probePath)
			if readErr != nil {
				return fmt.Errorf("worker probe must finish before host recovery: %w", readErr)
			}
			var report sandboxProbeReport
			if decodeErr := json.Unmarshal(data, &report); decodeErr != nil {
				return decodeErr
			}
			if report.DeliveryReceiptReadable || report.DeliveryReceiptDirectWriteSucceeded || report.DeliveryReceiptAPIWriteSucceeded {
				return fmt.Errorf("worker crossed delivery-receipt authority boundary: %+v", report)
			}
			if _, found, readErr := delivery.ReadControllerDeliveryReceipt(targetDir, runID); readErr != nil || found {
				return fmt.Errorf("worker unexpectedly created a controller receipt: found=%v err=%v", found, readErr)
			}
			if err := delivery.WriteControllerDeliveryReceipt(targetDir, deliveryRecord); err != nil {
				return fmt.Errorf("trusted host could not reconcile the interrupted delivery: %w", err)
			}
			stored, found, readErr := delivery.ReadControllerDeliveryReceipt(targetDir, runID)
			if readErr != nil || !found || stored.CommitSHA != deliveryRecord.CommitSHA || stored.PlanHash != deliveryRecord.PlanHash {
				return fmt.Errorf("trusted host cannot read its reconciled receipt: record=%+v found=%v err=%v", stored, found, readErr)
			}
			hostRecoveryCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	fakeOpenAI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fake-openai" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, "fake-openai-ok")
	}))
	defer fakeOpenAI.Close()
	engine.openAIEgressDial = func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", strings.TrimPrefix(fakeOpenAI.URL, "https://"))
	}
	if _, err := engine.Execute(context.Background(), Job{
		SchemaVersion: SchemaVersion, Operation: OperationRecover,
		RunID: "sandbox-probe", Feature: "probe", Task: "test worker filesystem boundary", TargetDir: target,
	}); err != nil {
		t.Fatalf("bubblewrap worker invocation failed (runtime must fail closed): %v", err)
	}
	if !hostRecoveryCalled {
		t.Fatal("trusted host receipt reconciliation did not run after sandboxed recovery")
	}
	data, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	var report sandboxProbeReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid probe report %q: %v", data, err)
	}
	if report.DatabaseReadable || report.WALReadable || report.SHMReadable || report.JournalReadable || report.LifecycleReadable || report.EventAuthorityProofReadable || report.LegacyApprovalReadable || report.CandidateMetadataReadable || report.CandidateEvidenceReadable || report.CandidateEvidenceDirectWriteSucceeded || report.UsageStateReadable || report.DeliveryStateReadable || report.DeliveryDirectWriteSucceeded || report.ContainmentStateReadable || report.ContainmentDirectWriteSucceeded {
		t.Fatalf("controller-owned state visible inside worker: %+v", report)
	}
	if report.BriefSourceReadable || report.BriefSourceWriteSucceeded || report.OtherRunBriefReadable || report.OtherRunBriefWriteSucceeded || !report.BriefAPIListReadSucceeded || !report.BriefAncestorRenameSucceeded || !report.BriefAPIPinnedWriteSucceeded || report.BriefRedirectedWriteSucceeded || !report.WorkerAPIAfterBriefAncestorProbe || !report.WorkspaceAfterBriefAncestorProbe {
		t.Fatalf("brief source/API boundary failed: %+v", report)
	}
	canonicalBriefRootAfter, err := os.Stat(canonicalBriefRoot)
	if err != nil || !os.SameFile(canonicalBriefRootBefore, canonicalBriefRootAfter) {
		t.Fatalf("worker ancestor probe changed the canonical brief store inode: err=%v", err)
	}
	redirectedAppend, err := os.ReadFile(filepath.Join(target, ".ai-team", "state", "briefs", "sandbox-probe", "0002-answer-ancestor-redirection-probe.md"))
	if err != nil || !strings.Contains(string(redirectedAppend), sandboxBriefAncestorProbeAnswer) {
		t.Fatalf("controller API did not persist the probe append on the canonical host store: content=%q err=%v", redirectedAppend, err)
	}
	if !report.RunDirectoryAbsentBeforeEvidenceStart || !report.EvidenceStartSucceeded {
		t.Fatalf("fresh evidence publication boundary failed: %+v", report)
	}
	if report.EventLogDirectReadable || report.EventLogDirectWriteSucceeded || !report.EventLogAPIReadAppendSucceeded ||
		!report.EventLogStateReplacementAPIPinned || !report.EventLogTeamReplacementAPIPinned ||
		!report.WorkerAPIAfterEventProbe || !report.WorkspaceAfterEventProbe {
		t.Fatalf("controller event authority/API boundary failed: %+v", report)
	}
	if report.CapabilityParentEnvironmentReadable || report.APISocketReplacementSucceeded || report.EgressSocketReplacementSucceeded ||
		!report.APISocketStillUsable || !report.EgressSocketStillUsable {
		t.Fatalf("worker capability process/socket boundary failed: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(target, ".ai-team", "runs", "sandbox-probe", "run.json")); err != nil {
		t.Fatalf("child evidence.Start did not publish the run on the host: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", "sandbox-probe", "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cloud run must not publish a worker-visible event mirror: %v", err)
	}
	canonicalEvents, err := (evidence.ControllerEventStore{TargetDir: target}).Read("sandbox-probe")
	if err != nil || len(canonicalEvents) != 4 {
		t.Fatalf("controller canonical event source lost probe events: len=%d err=%v", len(canonicalEvents), err)
	}
	briefAfter, err := os.ReadFile(filepath.Join(target, ".ai-team", "state", "briefs", "sandbox-probe", filepath.Base(brief.Version.Path)))
	if err != nil || string(briefAfter) != string(brief.Content) {
		t.Fatalf("worker changed or removed durable controller brief after child exit: content=%q err=%v", briefAfter, err)
	}
	otherRunBriefAfter, err := os.ReadFile(otherRunBriefPath)
	if err != nil || string(otherRunBriefAfter) != string(otherRunBriefBytes) {
		t.Fatalf("worker changed or removed another run's durable controller brief: content=%q err=%v", otherRunBriefAfter, err)
	}
	if !report.UsageAPIWriteSucceeded {
		t.Fatalf("worker could not publish usage through the controller API: %+v", report)
	}
	if !report.DeliveryAPIWriteSucceeded {
		t.Fatalf("worker could not submit terminal delivery through controller API: %+v", report)
	}
	if !report.CandidateEvidenceAPIWriteReadSucceeded {
		t.Fatalf("worker could not round-trip candidate evidence through controller API: %+v", report)
	}
	if report.AttemptManifestReadable || report.AttemptManifestDirectWriteSucceeded || !report.AttemptManifestAPIWriteReadSucceeded {
		t.Fatalf("controller attempt manifest direct/API boundary failed: %+v", report)
	}
	if _, err := manifestStore.ReadAttemptManifest("", "sandbox-probe", manifestSentinel.AttemptID); err != nil {
		t.Fatalf("worker directly modified controller attempt manifest: %v", err)
	}
	if stored, readErr := readControllerCandidateEvidence(candidateEvidenceRoot, probeCandidateEvidence.RunID, "review-candidate.json"); readErr != nil || stored.WorkspaceSHA256 != probeCandidateEvidence.WorkspaceSHA256 {
		t.Fatalf("controller candidate evidence sentinel was modified or lost: document=%+v err=%v", stored, readErr)
	}
	if report.AttestationStateReadable || report.AttestationDirectWriteSucceeded || !report.AttestationAPIWriteSucceeded {
		t.Fatalf("controller attestation isolation/API boundary failed: %+v", report)
	}
	if _, err := (attest.ControllerStore{TargetDir: target}).Read("sandbox-probe"); err != nil {
		t.Fatalf("host controller attestation was modified or lost: %v", err)
	}
	if !report.ContainmentAPIWriteSucceeded {
		t.Fatalf("worker could not submit containment receipt through controller API: %+v", report)
	}
	if _, err := (containment.ControllerReceiptStore{TargetDir: target}).Read("sandbox-probe"); err != nil {
		t.Fatalf("host controller containment receipt was modified or lost: %v", err)
	}
	if stored, found, readErr := delivery.ReadControllerTerminalRecord(target, deliveryRecord.RunID); readErr != nil || !found || stored.CommitSHA != deliveryRecord.CommitSHA {
		t.Fatalf("controller delivery sentinel changed or disappeared: record=%+v found=%v err=%v", stored, found, readErr)
	}
	if report.DeliveryReceiptReadable || report.DeliveryReceiptDirectWriteSucceeded || report.DeliveryReceiptAPIWriteSucceeded {
		t.Fatalf("controller delivery receipt must be hidden and read-only to the worker: %+v", report)
	}
	if stored, found, readErr := delivery.ReadControllerDeliveryReceipt(target, deliveryRecord.RunID); readErr != nil || !found || stored.CommitSHA != deliveryRecord.CommitSHA {
		t.Fatalf("controller delivery receipt sentinel changed or disappeared: record=%+v found=%v err=%v", stored, found, readErr)
	}
	if _, err := metrics.ReadUsageEnvelope(target, "sandbox-probe"); err != nil {
		t.Fatalf("controller did not retain worker usage after process exit: %v", err)
	}
	if !report.ControllerAPIReachable {
		t.Fatalf("scoped controller API unavailable over isolated network: %+v", report)
	}
	if !report.AdminControlPlaneCallRejected {
		t.Fatalf("admin/control-plane API call was not rejected by the worker API: %+v", report)
	}
	if !report.AuthSecretAbsent || !report.SigningKeyAbsent || !report.DatabasePasswordAbsent || !report.HostingWriteTokenAbsent {
		t.Fatalf("control-plane secret sentinel reached the worker environment: %+v", report)
	}
	if report.HostTCPReachable || report.OutboundTCPReachable {
		t.Fatalf("worker escaped its private network namespace: %+v", report)
	}
	if !report.TargetReadable || !report.TargetWritable || !report.WorktreeReadable || report.AgentRegistryWritable {
		t.Fatalf("unexpected workspace or agent-registry access: %+v", report)
	}
	if !report.OpenAIProxyReachable || !report.OpenAIDeniedOtherHost || !report.OpenAIDeniedOtherPort {
		t.Fatalf("OpenAI CONNECT proxy policy failed: %+v", report)
	}
}

func runOpenAIEgressProbe(t *testing.T) (bool, bool, bool) {
	t.Helper()
	socket, token := os.Getenv(openAIEgressSocketEnv), os.Getenv(openAIEgressTokenEnv)
	if socket == "" || token == "" {
		return false, false, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyURL, closeBridge, err := StartOpenAIEgressBridge(ctx, socket, token)
	if err != nil {
		return false, false, false
	}
	defer closeBridge()
	_ = os.Setenv("HTTP_PROXY", proxyURL)
	_ = os.Setenv("HTTPS_PROXY", proxyURL)
	_ = os.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")

	proxy, err := url.Parse(proxyURL)
	if err != nil {
		return false, false, false
	}
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // fake upstream only
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	allowedRequest, err := http.NewRequest(http.MethodGet, "https://api.openai.com/fake-openai", nil)
	if err != nil {
		return false, false, false
	}
	chosenProxy, err := http.ProxyFromEnvironment(allowedRequest)
	if err != nil || chosenProxy == nil || !strings.EqualFold(chosenProxy.Host, proxy.Host) {
		return false, false, false
	}
	response, err := client.Do(allowedRequest)
	if err != nil {
		return false, false, false
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	allowed := readErr == nil && response.StatusCode == http.StatusOK && string(body) == "fake-openai-ok"

	denied := func(target string) bool {
		request, reqErr := http.NewRequest(http.MethodGet, target, nil)
		if reqErr != nil {
			return false
		}
		selected, proxyErr := http.ProxyFromEnvironment(request)
		if proxyErr != nil || selected == nil || !strings.EqualFold(selected.Host, proxy.Host) {
			return false
		}
		result, requestErr := client.Do(request)
		if result != nil {
			_ = result.Body.Close()
		}
		return requestErr != nil
	}
	return allowed, denied("https://example.invalid/"), denied("https://api.openai.com:444/")
}

func TestBubblewrapRejectsHardLinkedControllerDatabaseAndSidecar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "controller.db")
	if err := os.WriteFile(dbPath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "database-alias")
	if err := os.Link(dbPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAndCheckDatabasePath(dbPath); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked database must fail closed, got %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	sidecar := dbPath + "-wal"
	if err := os.WriteFile(sidecar, []byte("wal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sidecar, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAndCheckDatabasePath(sidecar); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked sidecar must fail closed, got %v", err)
	}
}

func TestBubblewrapMasksRunAndRetainsUnixAPIOnlyWhenSocketSetupSucceeds(t *testing.T) {
	t.Run("command masks standard host sockets", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeBwrap := filepath.Join(fakeBin, "bwrap")
		if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin)
		target := makeBubblewrapTarget(t)
		home, temp := t.TempDir(), t.TempDir()
		command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
			filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + home, "TMPDIR=" + temp})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for i := 0; i+1 < len(command.Args); i++ {
			if command.Args[i] == "--tmpfs" && command.Args[i+1] == "/run" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bubblewrap command must mask standard host sockets at /run: %v", command.Args)
		}
		briefCommand, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
			filepath.Join(target, "controller.db"), "brief-mask-test", nil, []string{"HOME=" + home, "TMPDIR=" + temp})
		if err != nil {
			t.Fatal(err)
		}
		briefDir := filepath.Join(target, ".ai-team", "state", "briefs")
		foundBriefMask := false
		for i := 0; i+1 < len(briefCommand.Args); i++ {
			if briefCommand.Args[i] == "--tmpfs" && briefCommand.Args[i+1] == briefDir {
				foundBriefMask = true
				break
			}
		}
		if !foundBriefMask {
			t.Fatalf("bubblewrap command must mask the complete canonical brief root: %v", briefCommand.Args)
		}
		foundReadonlyBriefShadow := false
		for i := 0; i+2 < len(briefCommand.Args); i++ {
			if briefCommand.Args[i] == "--chmod" && briefCommand.Args[i+1] == "0555" && briefCommand.Args[i+2] == briefDir {
				foundReadonlyBriefShadow = true
				break
			}
		}
		if !foundReadonlyBriefShadow {
			t.Fatalf("bubblewrap brief shadow must be read-only: %v", briefCommand.Args)
		}
		eventRoot := filepath.Join(target, ".ai-team", "state", "events")
		foundEventMask, foundReadonlyEventShadow := false, false
		for i := 0; i+1 < len(briefCommand.Args); i++ {
			if briefCommand.Args[i] == "--tmpfs" && briefCommand.Args[i+1] == eventRoot {
				foundEventMask = true
			}
			if i+2 < len(briefCommand.Args) && briefCommand.Args[i] == "--chmod" && briefCommand.Args[i+1] == "0555" && briefCommand.Args[i+2] == eventRoot {
				foundReadonlyEventShadow = true
			}
		}
		if !foundEventMask || !foundReadonlyEventShadow {
			t.Fatalf("bubblewrap event authority root must be masked with a read-only shadow: %v", briefCommand.Args)
		}
		deliveryReceiptDir := filepath.Join(target, ".ai-team", "state", "delivery-receipts")
		foundReadonlyReceiptShadow := false
		for i := 0; i+1 < len(briefCommand.Args); i++ {
			if briefCommand.Args[i] == "--remount-ro" && briefCommand.Args[i+1] == deliveryReceiptDir {
				foundReadonlyReceiptShadow = true
				break
			}
		}
		if !foundReadonlyReceiptShadow {
			t.Fatalf("bubblewrap delivery receipt shadow must be read-only: %v", briefCommand.Args)
		}
		if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", "brief-mask-test")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bubblewrap brief setup created a fresh run evidence directory: err=%v", err)
		}
	})

	t.Run("Unix socket setup failure does not fall back to TCP or unsandboxed worker", func(t *testing.T) {
		target := makeBubblewrapTarget(t)
		workerPath := filepath.Join(t.TempDir(), "worker")
		if err := os.WriteFile(workerPath, []byte("#!/bin/sh\n: > \"$0.spawned\"\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		engine, err := NewProcessEngine([]string{workerPath}, target, filepath.Join(target, "controller.db"),
			WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}))
		if err != nil {
			t.Fatal(err)
		}
		// Inject failure at the exact controller-owned socket-directory boundary;
		// TMPDIR is private and the placement helper deliberately uses /tmp or
		// /var/tmp, so manipulating TMPDIR cannot reliably cause this failure.
		socketSetupErr := errors.New("injected socket-directory setup failure")
		socketDirectoryAttempts := 0
		engine.controlSocketDirCreator = func(gotTarget, home, temp string) (string, error) {
			socketDirectoryAttempts++
			if gotTarget != engine.target || home == "" || temp == "" {
				t.Fatalf("socket directory creator received target=%q home=%q temp=%q", gotTarget, home, temp)
			}
			return "", socketSetupErr
		}
		egressDialAttempts := 0
		engine.openAIEgressDial = func(context.Context) (net.Conn, error) {
			egressDialAttempts++
			return nil, errors.New("unexpected OpenAI egress dial")
		}
		// Exercise the bubblewrap execution path without requiring namespace
		// creation. Any attempted child execution writes a sentinel next to the
		// executable, regardless of whether a sandbox wrapper is present.
		engine.bubblewrap = true
		_, err = engine.Start(context.Background(), pipeline.RunConfig{
			RunID: "socket-setup-failure", Feature: "probe", TaskDesc: "test fail-closed socket setup", TargetDir: target,
		})
		if !errors.Is(err, socketSetupErr) || !strings.Contains(err.Error(), "worker controller socket directory") {
			t.Fatalf("controller API Unix socket setup failure must stop the worker invocation, got %v", err)
		}
		if socketDirectoryAttempts != 1 {
			t.Fatalf("controller socket directory setup attempts=%d, want exactly one fail-closed attempt", socketDirectoryAttempts)
		}
		if egressDialAttempts != 0 {
			t.Fatalf("OpenAI egress dial attempts=%d after socket setup failed", egressDialAttempts)
		}
		if _, err := os.Lstat(workerPath + ".spawned"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("worker executable ran after controller socket setup failed: err=%v", err)
		}
	})
}

func TestBubblewrapMountsControllerCandidateEvidenceReadOnly(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	for _, tc := range []struct {
		name  string
		files []string
	}{
		{name: "review candidate only", files: []string{"review-candidate.json"}},
		{name: "both candidate documents", files: []string{"review-candidate.json", "verification-candidate.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := makeBubblewrapTarget(t)
			runID := "candidate-mount-test"
			runDir := filepath.Join(target, ".ai-team", "state", "evidence", runID)
			if err := os.MkdirAll(runDir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(runDir, name), []byte("controller-owned candidate evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), runID, nil,
				[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}

			wantPaths := make(map[string]bool, len(tc.files))
			for _, name := range tc.files {
				wantPaths[filepath.Join(runDir, name)] = false
			}
			dirMounts := 0
			for i := 0; i+2 < len(command.Args); i++ {
				if command.Args[i] == "--dir" && command.Args[i+1] == runDir {
					dirMounts++
				}
				if command.Args[i] == "--ro-bind" && command.Args[i+1] == "/dev/null" {
					if _, expected := wantPaths[command.Args[i+2]]; expected {
						wantPaths[command.Args[i+2]] = true
					}
				}
			}
			if dirMounts != 1 {
				t.Fatalf("candidate evidence run directory must be recreated exactly once after its private mask; got %d mounts in %v", dirMounts, command.Args)
			}
			for path, found := range wantPaths {
				if !found {
					t.Errorf("controller candidate evidence must be masked with a read-only /dev/null bind: %s; args=%v", path, command.Args)
				}
			}
		})
	}
}

func TestBubblewrapRejectsSymlinkedControllerCandidateEvidence(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	runDir := filepath.Join(target, ".ai-team", "state", "evidence", "sandbox-test")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(outside, []byte("outside controller state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runDir, "review-candidate.json")); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil,
		[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "private worker path") || !strings.Contains(err.Error(), "must contain only regular files and directories") {
		t.Fatalf("symlinked controller candidate evidence must be rejected by private directory validation, got %v", err)
	}
}

func TestBubblewrapRejectsNonRegularControllerAttestationRecord(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	attestationDir := filepath.Join(target, ".ai-team", "state", "attestation")
	if err := os.MkdirAll(attestationDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(attestationDir, "sandbox-test.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "controller attestation path") || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("non-regular controller attestation record must fail closed, got %v", err)
	}
}

func TestBubblewrapRejectsNonRegularControllerContainmentReceipt(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	containmentDir := filepath.Join(target, ".ai-team", "state", "containment")
	if err := os.MkdirAll(containmentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(containmentDir, "sandbox-test.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "controller containment path") || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("non-regular controller containment receipt must fail closed, got %v", err)
	}
}

func TestBubblewrapRejectsUnsafeControllerContainmentDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seedState func(t *testing.T, target string)
		want      string
	}{
		{
			name: "symlinked directory",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				stateDir := filepath.Join(target, ".ai-team", "state")
				outside := filepath.Join(t.TempDir(), "containment")
				if err := os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(stateDir, "containment")); err != nil {
					t.Fatal(err)
				}
			},
			want: "prepare controller containment mount",
		},
		{
			name: "hard-linked entry",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				containmentDir := filepath.Join(target, ".ai-team", "state", "containment")
				if err := os.Mkdir(containmentDir, 0700); err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(containmentDir, "sentinel.json")
				if err := os.WriteFile(entry, []byte("controller sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(entry, filepath.Join(target, "sentinel-alias.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "hard links",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeBwrap := filepath.Join(fakeBin, "bwrap")
			if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			tc.seedState(t, target)
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe controller containment directory must fail closed with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBubblewrapRejectsUnsafeControllerAttestationDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seedState func(t *testing.T, target string)
		want      string
	}{
		{
			name: "symlinked directory",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				stateDir := filepath.Join(target, ".ai-team", "state")
				outside := filepath.Join(t.TempDir(), "attestation")
				if err := os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(stateDir, "attestation")); err != nil {
					t.Fatal(err)
				}
			},
			want: "prepare controller attestation mount",
		},
		{
			name: "hard-linked entry",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				attestationDir := filepath.Join(target, ".ai-team", "state", "attestation")
				if err := os.Mkdir(attestationDir, 0700); err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(attestationDir, "sentinel.json")
				if err := os.WriteFile(entry, []byte("controller sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(entry, filepath.Join(target, "sentinel-alias.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "hard links",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeBwrap := filepath.Join(fakeBin, "bwrap")
			if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			tc.seedState(t, target)
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe controller attestation directory must fail closed with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBubblewrapPathAndFileValidationFailsClosed(t *testing.T) {
	t.Run("command builder rejects invalid run ids before filesystem or runtime lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		for _, runID := range []string{"", "../escape", "bad\nid"} {
			t.Run(fmt.Sprintf("run id %q", runID), func(t *testing.T) {
				_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"),
					filepath.Join(t.TempDir(), "missing-target"), "unused.db", runID, nil, nil)
				if err == nil || !strings.Contains(err.Error(), "bubblewrap run id") {
					t.Fatalf("invalid run id must be rejected before target or bwrap lookup, got %v", err)
				}
			})
		}
	})

	t.Run("missing bubblewrap", func(t *testing.T) {
		path := t.TempDir()
		t.Setenv("PATH", path)
		if err := checkBubblewrapAvailable(); err == nil || !strings.Contains(err.Error(), "requires bubblewrap") {
			t.Fatalf("missing bubblewrap must be rejected, got %v", err)
		}
		if _, err := NewProcessEngine([]string{"worker"}, makeBubblewrapTarget(t), "controller.db", WithLinuxBubblewrapIsolation()); err == nil || !strings.Contains(err.Error(), "requires bubblewrap") {
			t.Fatalf("bubblewrap option must fail closed when runtime is missing, got %v", err)
		}
	})
	t.Run("command builder rejects root before runtime lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), string(filepath.Separator), "unused.db", "sandbox-test", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "filesystem root") {
			t.Fatalf("root workspace must fail before looking up bwrap, got %v", err)
		}
	})
	t.Run("command builder reports missing runtime", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		target := makeBubblewrapTarget(t)
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=/tmp", "TMPDIR=/tmp"})
		if err == nil || !strings.Contains(err.Error(), "bubblewrap unavailable") {
			t.Fatalf("missing bwrap runtime must fail closed, got %v", err)
		}
	})

	t.Run("invalid worker command environment and paths", func(t *testing.T) {
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is installed by Linux CI; validation cases require it on PATH")
		}
		target := makeBubblewrapTarget(t)
		worker := exec.Command("/bin/true")
		validEnv := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
		for _, tc := range []struct {
			name       string
			env        []string
			agentPaths []string
			target     string
			dbPath     string
			want       string
		}{
			{name: "missing home", env: []string{"TMPDIR=" + t.TempDir()}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "HOME and TMPDIR"},
			{name: "relative temp", env: []string{"HOME=" + t.TempDir(), "TMPDIR=relative"}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "HOME and TMPDIR"},
			{name: "missing agent registry", env: validEnv, agentPaths: []string{filepath.Join(target, "missing-agents")}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "agent registry path"},
			{name: "missing lifecycle directory", env: validEnv, target: t.TempDir(), dbPath: filepath.Join(target, "controller.db"), want: "private worker path"},
			{name: "database parent unavailable", env: validEnv, target: target, dbPath: filepath.Join(target, "missing", "controller.db"), want: "resolve database parent"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := bubblewrapWorkerCommand(context.Background(), worker, tc.target, tc.dbPath, "sandbox-test", tc.agentPaths, tc.env)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q failure, got %v", tc.want, err)
				}
			})
		}
	})
}

func TestBubblewrapCommandBuilderRejectsProtectedAliasesAndApprovalSymlinks(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is installed by Linux CI; command validation requires it on PATH")
	}
	target := makeBubblewrapTarget(t)
	dbPath := filepath.Join(target, ".ai-team", "controller.db")
	if err := os.WriteFile(dbPath, []byte("controller secret"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(target, "controller-alias.db")
	if err := os.Link(dbPath, alias); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, "sandbox-test", nil, env)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("command builder must reject a DB alias before masking: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	approvalPath := filepath.Join(target, ".ai-team", "state", "approvals")
	approvalTarget := filepath.Join(t.TempDir(), "approvals")
	if err := os.Mkdir(approvalTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(approvalTarget, approvalPath); err != nil {
		t.Fatal(err)
	}
	_, err = bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, "sandbox-test", nil, env)
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("command builder must reject a symlinked approval directory: %v", err)
	}
}

func TestBubblewrapPrivateStateRejectsUnsafeEntries(t *testing.T) {
	t.Run("missing required directory", func(t *testing.T) {
		if err := appendPrivateDirectoryMount(&[]string{}, filepath.Join(t.TempDir(), "missing"), true); err == nil {
			t.Fatal("missing required private directory must fail closed")
		}
	})
	t.Run("missing optional directory", func(t *testing.T) {
		args := []string{}
		if err := appendPrivateDirectoryMount(&args, filepath.Join(t.TempDir(), "missing"), false); err != nil {
			t.Fatalf("missing optional private directory should be ignored: %v", err)
		}
		if len(args) != 0 {
			t.Fatalf("unexpected mount for missing optional directory: %v", args)
		}
	})
	t.Run("non-directory path", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(file, []byte("state"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := appendPrivateDirectoryMount(&[]string{}, file, true); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("non-directory state path must fail closed: %v", err)
		}
	})
	t.Run("symlink entry", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink("missing-target", filepath.Join(dir, "linked.json")); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateDirectory(dir); err == nil || !strings.Contains(err.Error(), "regular files and directories") {
			t.Fatalf("symlink state entry must fail closed: %v", err)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if err := verifyPrivateDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("missing private directory must fail closed")
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission checks are ineffective as root")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
		if err := verifyPrivateDirectory(dir); err == nil {
			t.Fatal("unreadable private directory must fail closed")
		}
	})
	t.Run("looping database symlink", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExistingPath(loop); err == nil {
			t.Fatal("looping database path must fail closed")
		}
	})
	t.Run("missing database sidecar parent", func(t *testing.T) {
		if _, err := resolveAndCheckDatabasePath(filepath.Join(t.TempDir(), "missing", "database.db")); err == nil {
			t.Fatal("missing database parent must fail closed")
		}
	})
	t.Run("missing database path with existing parent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "database.db")
		resolved, err := resolveAndCheckDatabasePath(path)
		if err != nil || resolved != path {
			t.Fatalf("missing database leaf should resolve to its canonical future path: %q, %v", resolved, err)
		}
	})
	t.Run("database directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "database")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveAndCheckDatabasePath(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("database directory must fail closed: %v", err)
		}
	})
}

func TestBubblewrapMasksOrdinaryControllerDatabase(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	target := makeBubblewrapTarget(t)
	dbPath := filepath.Join(target, ".ai-team", "controller.db")
	if err := os.WriteFile(dbPath, []byte("ordinary controller database"), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		dbPath, "ordinary-database-mask-test", nil,
		[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err != nil {
		t.Fatalf("builder must allow a regular controller database without aliases: %v", err)
	}

	wantPaths := []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"}
	for _, path := range wantPaths {
		found := false
		for i := 0; i+2 < len(command.Args); i++ {
			if command.Args[i] == "--ro-bind" && command.Args[i+1] == "/dev/null" && command.Args[i+2] == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("controller database path must be masked with --ro-bind /dev/null: %s; args=%v", path, command.Args)
		}
	}
}

func makeBubblewrapTarget(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "state", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestBubblewrapRejectsMissingAndNonDirectoryWorkspace(t *testing.T) {
	if _, err := resolveBubblewrapTarget(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace must fail closed")
	}
	file := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBubblewrapTarget(file); err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("non-directory workspace must fail closed: %v", err)
	}
}

func TestBubblewrapRejectsRunOverlappingEnvironmentAndPartialOpenAIEgress(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	worker := exec.Command("worker")
	baseEnv := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	build := func(env []string) error {
		t.Helper()
		_, err := bubblewrapWorkerCommand(context.Background(), worker, target,
			filepath.Join(target, "controller.db"), "sandbox-test", nil, env)
		return err
	}

	t.Run("HOME cannot reopen private run mount", func(t *testing.T) {
		err := build([]string{"HOME=/run", baseEnv[1]})
		if err == nil || !strings.Contains(err.Error(), "worker HOME") || !strings.Contains(err.Error(), "overlaps /run") {
			t.Fatalf("HOME overlapping the private /run mount must fail closed, got %v", err)
		}
	})
	t.Run("TMPDIR cannot reopen private run mount", func(t *testing.T) {
		err := build([]string{baseEnv[0], "TMPDIR=/run"})
		if err == nil || !strings.Contains(err.Error(), "worker TMPDIR") || !strings.Contains(err.Error(), "overlaps /run") {
			t.Fatalf("TMPDIR overlapping the private /run mount must fail closed, got %v", err)
		}
	})
	t.Run("OpenAI socket requires capability", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "openai-egress.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatalf("create valid OpenAI proxy socket fixture: %v", err)
		}
		defer func() { _ = listener.Close() }()
		env := append(append([]string(nil), baseEnv...), openAIEgressSocketEnv+"="+socket)
		err = build(env)
		if err == nil || !strings.Contains(err.Error(), "socket and capability must be configured together") {
			t.Fatalf("partial OpenAI egress configuration must fail closed, got %v", err)
		}
	})
	t.Run("OpenAI capability requires socket", func(t *testing.T) {
		env := append(append([]string(nil), baseEnv...), openAIEgressTokenEnv+"=scoped-capability")
		err := build(env)
		if err == nil || !strings.Contains(err.Error(), "socket and capability must be configured together") {
			t.Fatalf("partial OpenAI egress configuration must fail closed, got %v", err)
		}
	})
}

func TestBubblewrapRejectsFilesystemRootAsTarget(t *testing.T) {
	for _, target := range []string{string(filepath.Separator), filepath.Join(string(filepath.Separator), ".")} {
		t.Run(target, func(t *testing.T) {
			if resolved, err := resolveBubblewrapTarget(target); err == nil {
				t.Fatalf("filesystem root target accepted as %q", resolved)
			} else if !strings.Contains(err.Error(), "filesystem root") {
				t.Fatalf("expected filesystem root rejection, got %v", err)
			}
		})
	}

	alias := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(string(filepath.Separator), alias); err != nil {
		t.Fatal(err)
	}
	if resolved, err := resolveBubblewrapTarget(alias); err == nil {
		t.Fatalf("symlink to filesystem root accepted as %q", resolved)
	} else if !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("expected symlink root rejection, got %v", err)
	}
}

func TestBubblewrapRejectsRunMountReopeningPaths(t *testing.T) {
	t.Run("missing bind source", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-registry")
		if err := rejectRunBindPath(missing, "agent registry path"); err == nil || !strings.Contains(err.Error(), "resolve agent registry path bind path") {
			t.Fatalf("missing bind source must fail before mount construction, got %v", err)
		}
	})
	t.Run("direct run target", func(t *testing.T) {
		if _, err := resolveBubblewrapTarget("/run"); err == nil || !strings.Contains(err.Error(), "/run") {
			t.Fatalf("/run workspace must be rejected before mount construction, got %v", err)
		}
	})
	t.Run("symlink target to run", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "run-target")
		if err := os.Symlink("/run", alias); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveBubblewrapTarget(alias); err == nil || !strings.Contains(err.Error(), "/run") {
			t.Fatalf("symlink target resolving to /run must be rejected, got %v", err)
		}
	})
	t.Run("agent registry bind cannot overlap run", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeBwrap := filepath.Join(fakeBin, "bwrap")
		if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin)
		alias := filepath.Join(t.TempDir(), "root-agent-registry")
		if err := os.Symlink("/", alias); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			path string
		}{
			{name: "run", path: "/run"},
			{name: "filesystem root", path: "/"},
			{name: "symlink to filesystem root", path: alias},
		} {
			t.Run(tc.name, func(t *testing.T) {
				target := makeBubblewrapTarget(t)
				home, temp := t.TempDir(), t.TempDir()
				_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
					filepath.Join(target, "controller.db"), "sandbox-test", []string{tc.path}, []string{"HOME=" + home, "TMPDIR=" + temp})
				if err == nil || !strings.Contains(err.Error(), "agent registry path") || !strings.Contains(err.Error(), "overlaps /run") {
					t.Fatalf("agent registry bind %q must be rejected, got %v", tc.path, err)
				}
			})
		}
	})
}

func TestBubblewrapRejectsHardLinkedPrivateState(t *testing.T) {
	target := t.TempDir()
	privateDir := filepath.Join(target, "private")
	if err := os.Mkdir(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(privateDir, "run.json")
	if err := os.WriteFile(statePath, []byte("controller state"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(target, "visible-alias.json")
	if err := os.Link(statePath, alias); err != nil {
		t.Fatal(err)
	}
	args := []string{"--ro-bind", "/", "/"}
	if err := appendPrivateDirectoryMount(&args, privateDir, true); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked private state must fail closed, got %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("failed mount must not be appended, args=%v", args)
	}
}

type sandboxProbeReport struct {
	DatabaseReadable                       bool `json:"database_readable"`
	WALReadable                            bool `json:"wal_readable"`
	SHMReadable                            bool `json:"shm_readable"`
	JournalReadable                        bool `json:"journal_readable"`
	LifecycleReadable                      bool `json:"lifecycle_readable"`
	EventAuthorityProofReadable            bool `json:"event_authority_proof_readable"`
	LegacyApprovalReadable                 bool `json:"legacy_approval_readable"`
	CandidateMetadataReadable              bool `json:"candidate_metadata_readable"`
	CandidateEvidenceReadable              bool `json:"candidate_evidence_readable"`
	CandidateEvidenceDirectWriteSucceeded  bool `json:"candidate_evidence_direct_write_succeeded"`
	CandidateEvidenceAPIWriteReadSucceeded bool `json:"candidate_evidence_api_write_read_succeeded"`
	AttemptManifestReadable                bool `json:"attempt_manifest_readable"`
	AttemptManifestDirectWriteSucceeded    bool `json:"attempt_manifest_direct_write_succeeded"`
	AttemptManifestAPIWriteReadSucceeded   bool `json:"attempt_manifest_api_write_read_succeeded"`
	UsageStateReadable                     bool `json:"usage_state_readable"`
	DeliveryStateReadable                  bool `json:"delivery_state_readable"`
	DeliveryDirectWriteSucceeded           bool `json:"delivery_direct_write_succeeded"`
	DeliveryAPIWriteSucceeded              bool `json:"delivery_api_write_succeeded"`
	DeliveryReceiptReadable                bool `json:"delivery_receipt_readable"`
	DeliveryReceiptDirectWriteSucceeded    bool `json:"delivery_receipt_direct_write_succeeded"`
	DeliveryReceiptAPIWriteSucceeded       bool `json:"delivery_receipt_api_write_succeeded"`
	AttestationStateReadable               bool `json:"attestation_state_readable"`
	AttestationDirectWriteSucceeded        bool `json:"attestation_direct_write_succeeded"`
	AttestationAPIWriteSucceeded           bool `json:"attestation_api_write_succeeded"`
	ContainmentStateReadable               bool `json:"containment_state_readable"`
	ContainmentDirectWriteSucceeded        bool `json:"containment_direct_write_succeeded"`
	ContainmentAPIWriteSucceeded           bool `json:"containment_api_write_succeeded"`
	UsageAPIWriteSucceeded                 bool `json:"usage_api_write_succeeded"`
	BriefSourceReadable                    bool `json:"brief_source_readable"`
	BriefSourceWriteSucceeded              bool `json:"brief_source_write_succeeded"`
	OtherRunBriefReadable                  bool `json:"other_run_brief_readable"`
	OtherRunBriefWriteSucceeded            bool `json:"other_run_brief_write_succeeded"`
	BriefAPIListReadSucceeded              bool `json:"brief_api_list_read_succeeded"`
	BriefAncestorRenameSucceeded           bool `json:"brief_ancestor_rename_succeeded"`
	BriefAPIPinnedWriteSucceeded           bool `json:"brief_api_pinned_write_succeeded"`
	BriefRedirectedWriteSucceeded          bool `json:"brief_redirected_write_succeeded"`
	WorkerAPIAfterBriefAncestorProbe       bool `json:"worker_api_after_brief_ancestor_probe"`
	WorkspaceAfterBriefAncestorProbe       bool `json:"workspace_after_brief_ancestor_probe"`
	RunDirectoryAbsentBeforeEvidenceStart  bool `json:"run_directory_absent_before_evidence_start"`
	EvidenceStartSucceeded                 bool `json:"evidence_start_succeeded"`
	EventLogDirectReadable                 bool `json:"event_log_direct_readable"`
	EventLogDirectWriteSucceeded           bool `json:"event_log_direct_write_succeeded"`
	EventLogAPIReadAppendSucceeded         bool `json:"event_log_api_read_append_succeeded"`
	EventLogStateReplacementAPIPinned      bool `json:"event_log_state_replacement_api_pinned"`
	EventLogTeamReplacementAPIPinned       bool `json:"event_log_team_replacement_api_pinned"`
	CapabilityParentEnvironmentReadable    bool `json:"capability_parent_environment_readable"`
	APISocketReplacementSucceeded          bool `json:"api_socket_replacement_succeeded"`
	EgressSocketReplacementSucceeded       bool `json:"egress_socket_replacement_succeeded"`
	APISocketStillUsable                   bool `json:"api_socket_still_usable"`
	EgressSocketStillUsable                bool `json:"egress_socket_still_usable"`
	WorkerAPIAfterEventProbe               bool `json:"worker_api_after_event_probe"`
	WorkspaceAfterEventProbe               bool `json:"workspace_after_event_probe"`
	WorktreeReadable                       bool `json:"worktree_readable"`
	TargetReadable                         bool `json:"target_readable"`
	TargetWritable                         bool `json:"target_writable"`
	AgentRegistryWritable                  bool `json:"agent_registry_writable"`
	ControllerAPIReachable                 bool `json:"controller_api_reachable"`
	AdminControlPlaneCallRejected          bool `json:"admin_control_plane_call_rejected"`
	AuthSecretAbsent                       bool `json:"auth_secret_absent"`
	SigningKeyAbsent                       bool `json:"signing_key_absent"`
	DatabasePasswordAbsent                 bool `json:"database_password_absent"`
	HostingWriteTokenAbsent                bool `json:"hosting_write_token_absent"`
	HostTCPReachable                       bool `json:"host_tcp_reachable"`
	OutboundTCPReachable                   bool `json:"outbound_tcp_reachable"`
	OpenAIProxyReachable                   bool `json:"openai_proxy_reachable"`
	OpenAIDeniedOtherHost                  bool `json:"openai_denied_other_host"`
	OpenAIDeniedOtherPort                  bool `json:"openai_denied_other_port"`
}

// TestBubblewrapWorkerProbeHelper is executed as the child command by the
// integration test. It reports only whether protected sentinels were
// readable, then emits the normal worker result protocol.
func TestBubblewrapWorkerProbeHelper(t *testing.T) {
	if os.Getenv("AI_TEAM_BUBBLEWRAP_PROBE") != "1" {
		return
	}
	args := argsAfterDoubleDash(os.Args)
	value := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	job, err := DecodeJob(os.Stdin, value("--target"))
	if err != nil {
		t.Fatalf("decode probe job: %v", err)
	}
	if err := ProtectWorkerProcess(); err != nil {
		t.Fatalf("protect probe worker process: %v", err)
	}
	capabilityParentEnvironmentReadable := probeChildCanReadParentCapabilities(t)
	apiSocketReplacementSucceeded := probeSocketReplacement(os.Getenv(workerAPISocketEnv))
	egressSocketReplacementSucceeded := probeSocketReplacement(os.Getenv(openAIEgressSocketEnv))
	dbData, dbErr := os.ReadFile(value("--probe-db"))
	walData, walErr := os.ReadFile(value("--probe-wal"))
	shmData, shmErr := os.ReadFile(value("--probe-shm"))
	journalData, journalErr := os.ReadFile(value("--probe-journal"))
	lifecycleData, lifecycleErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "runs", "controller-state.json"))
	eventAuthorityProof, eventAuthorityProofErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "runs", "event-log-authority", "controller-sentinel.json"))
	approvalData, approvalErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "approvals", "pending.json"))
	candidateData, candidateErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "candidates", "controller-sentinel.json"))
	candidateEvidencePath := filepath.Join(job.TargetDir, ".ai-team", "state", "evidence", job.RunID, "review-candidate.json")
	candidateEvidenceData, candidateEvidenceErr := os.ReadFile(candidateEvidencePath)
	candidateEvidenceDirectWriteErr := os.WriteFile(candidateEvidencePath, []byte("worker-overwrite-attempt"), 0600)
	usageData, usageErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "usage", "controller-sentinel.json"))
	attemptManifestPath := filepath.Join(job.TargetDir, ".ai-team", "state", "attempt-manifests", job.RunID, "controller-sentinel.json")
	attemptManifestData, attemptManifestErr := os.ReadFile(attemptManifestPath)
	attemptManifestDirectWriteErr := os.WriteFile(attemptManifestPath, []byte("worker-overwrite-attempt"), 0600)
	deliveryPath := filepath.Join(job.TargetDir, ".ai-team", "state", "delivery", job.RunID+".json")
	deliveryData, deliveryErr := os.ReadFile(deliveryPath)
	deliveryDirectWriteErr := os.WriteFile(deliveryPath, []byte("worker-overwrite-attempt"), 0600)
	deliveryReceiptPath := filepath.Join(job.TargetDir, ".ai-team", "state", "delivery-receipts", job.RunID+".json")
	deliveryReceiptData, deliveryReceiptErr := os.ReadFile(deliveryReceiptPath)
	deliveryReceiptDirectWriteErr := os.WriteFile(deliveryReceiptPath, []byte("worker-forged-receipt"), 0600)
	attestationPath := filepath.Join(job.TargetDir, ".ai-team", "state", "attestation", job.RunID+".json")
	attestationData, attestationErr := os.ReadFile(attestationPath)
	attestationDirectWriteErr := os.WriteFile(attestationPath, []byte("worker-overwrite-attempt"), 0600)
	containmentPath := filepath.Join(job.TargetDir, ".ai-team", "state", "containment", job.RunID+".json")
	containmentData, containmentErr := os.ReadFile(containmentPath)
	containmentDirectWriteErr := os.WriteFile(containmentPath, []byte("worker-overwrite-attempt"), 0600)
	briefPath := filepath.Join(job.TargetDir, ".ai-team", "state", "briefs", job.RunID, "0001-intention.md")
	_, briefErr := os.ReadFile(briefPath)
	briefWriteErr := os.WriteFile(briefPath, []byte("worker-overwrite-attempt"), 0600)
	otherRunBriefPath := filepath.Join(job.TargetDir, ".ai-team", "state", "briefs", "sandbox-other-run", "0001-intention.md")
	_, otherRunBriefErr := os.ReadFile(otherRunBriefPath)
	otherRunBriefWriteErr := os.WriteFile(otherRunBriefPath, []byte("worker-overwrite-attempt"), 0600)
	runEvidenceRoot := filepath.Join(job.TargetDir, ".ai-team", "runs")
	runEvidenceDir := filepath.Join(runEvidenceRoot, job.RunID)
	_, runEvidenceStatErr := os.Lstat(runEvidenceDir)
	runDirectoryAbsentBeforeEvidenceStart := errors.Is(runEvidenceStatErr, os.ErrNotExist)
	evidenceStartSucceeded := false
	worktreeData, worktreeErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "worktrees", "probe", "visible.txt"))
	targetData, targetErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
	writeErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-write.txt"), []byte("worker-write"), 0600)
	agentWriteErr := os.WriteFile(filepath.Join(job.TargetDir, ".ai-team", "agents", "role.md"), []byte("modified"), 0600)
	apiReachable := false
	adminControlPlaneCallRejected := false
	usageAPIWriteSucceeded := false
	deliveryAPIWriteSucceeded := false
	deliveryReceiptAPIWriteSucceeded := false
	attestationAPIWriteSucceeded := false
	containmentAPIWriteSucceeded := false
	candidateEvidenceAPIWriteReadSucceeded := false
	attemptManifestAPIWriteReadSucceeded := false
	briefAPIListReadSucceeded := false
	briefAncestorRenameSucceeded := false
	briefAPIPinnedWriteSucceeded := false
	briefRedirectedWriteSucceeded := false
	workerAPIAfterBriefAncestorProbe := false
	workspaceAfterBriefAncestorProbe := false
	eventLogDirectReadable := false
	eventLogDirectWriteSucceeded := false
	eventLogAPIReadAppendSucceeded := false
	eventLogStateReplacementAPIPinned := false
	eventLogTeamReplacementAPIPinned := false
	workerAPIAfterEventProbe := false
	workspaceAfterEventProbe := false
	apiSocketStillUsable := false
	if port, portErr := NewWorkerAPIPort(job); portErr == nil {
		apiSocketStillUsable = port.call("approval.list", workerAPICall{RunID: job.RunID}, new([]approval.PendingApproval)) == nil
		eventLog := NewWorkerAPIEventLog(port)
		var evidenceStore *evidence.Store
		if runDirectoryAbsentBeforeEvidenceStart {
			var startErr error
			evidenceStore, startErr = evidence.StartWithEventLog(runEvidenceRoot, evidence.RunManifest{
				RunID: job.RunID, Feature: "bubblewrap-probe", TargetDir: job.TargetDir, StartedAt: time.Now().UTC(),
				ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
			}, eventLog)
			evidenceStartSucceeded = startErr == nil
			if startErr == nil {
				startErr = evidenceStore.Append(evidence.Event{Type: "run_started", Timestamp: time.Now().UTC()})
			}
			if startErr == nil {
				before, readErr := eventLog.Read(job.RunID)
				if readErr == nil && len(before) == 1 {
					startErr = evidenceStore.Append(evidence.Event{Type: "run_paused", Timestamp: time.Now().UTC(), Data: map[string]any{"status": "probe"}})
					if startErr == nil {
						after, afterErr := eventLog.Read(job.RunID)
						eventLogAPIReadAppendSucceeded = afterErr == nil && len(after) == 2
					}
				}
			}
		}
		eventPath := filepath.Join(job.TargetDir, ".ai-team", "state", "events", job.RunID, "events.jsonl")
		_, directReadErr := os.ReadFile(eventPath)
		eventLogDirectReadable = directReadErr == nil
		eventLogDirectWriteSucceeded = os.WriteFile(eventPath, []byte("worker-event-forgery"), 0600) == nil
		var approvals []approval.PendingApproval
		apiReachable = port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvals) == nil && len(approvals) == 1 && approvals[0].ID == sandboxBriefAncestorProbeApprovalID
		adminControlPlaneCallRejected = isExpectedAdminControlPlaneRejection(port.call("admin.control_plane", workerAPICall{RunID: job.RunID}, nil))
		started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
		usageEnvelope := metrics.Build(job.RunID, "probe", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
		usageAPIWriteSucceeded = port.call("usage.envelope.write", workerAPICall{Usage: usageEnvelope}, nil) == nil
		deliveryRecord := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: job.RunID, Feature: "probe", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
		deliveryAPIWriteSucceeded = NewWorkerAPITerminalRecordWriter(port).WriteTerminalRecord(deliveryRecord) == nil
		deliveryReceiptAPIWriteSucceeded = port.call("delivery.receipt.write", workerAPICall{TerminalRecord: deliveryRecord}, nil) == nil
		workerStatement := &attest.Statement{Type: attest.StatementType, PredicateType: attest.PredicateTypeV1, Predicate: attest.Predicate{SchemaVersion: attest.PredicateSchemaVersion, RunID: job.RunID}}
		attestationAPIWriteSucceeded = NewWorkerAPIAttestationWriter(port).WriteAttestation(workerStatement) == nil
		containmentAPIWriteSucceeded = NewWorkerAPIContainmentReceiptWriter(port).WriteContainmentReceipt(containment.DefaultTrustedLocalReceipt()) == nil
		candidateEvidenceStore := NewWorkerAPICandidateEvidenceStore(port)
		probeDocument := pipeline.CandidateEvidence{
			SchemaVersion: 1, RunID: job.RunID, Purpose: "semantic_code_review",
			WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: []pipeline.CandidateFile{},
			Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
		}
		if writeErr := candidateEvidenceStore.WriteCandidateEvidence("review-candidate.json", probeDocument); writeErr == nil {
			loaded, readErr := candidateEvidenceStore.ReadCandidateEvidence("review-candidate.json")
			candidateEvidenceAPIWriteReadSucceeded = readErr == nil && loaded.WorkspaceSHA256 == probeDocument.WorkspaceSHA256
		}
		attemptManifestStore := NewWorkerAPIAttemptManifestStore(port)
		manifestNow := time.Now().UTC()
		probeManifest := evidence.AttemptManifest{SchemaVersion: evidence.SchemaVersion, RunID: job.RunID, AttemptID: "api-probe", Stage: "probe", StageIndex: 1,
			StartedAt: manifestNow.Add(-time.Minute), FinishedAt: manifestNow, Status: "completed", Execution: "success", Decision: "continue", Outcome: "success"}
		if writeErr := attemptManifestStore.WriteAttemptManifest(probeManifest); writeErr == nil {
			loaded, readErr := attemptManifestStore.ReadAttemptManifest("", job.RunID, probeManifest.AttemptID)
			attemptManifestAPIWriteReadSucceeded = readErr == nil && len(loaded) > 0
		}
		briefs := NewWorkerAPIBriefs(port)
		_, createErr := briefs.CreateInitial(job.RunID, job.Task)
		versions, listErr := briefs.List(job.RunID)
		if listErr == nil && len(versions) == 1 {
			document, readErr := briefs.Read(job.RunID, versions[0].ID)
			briefAPIListReadSucceeded = createErr == nil && readErr == nil && string(document.Content) == "# Исходное намерение\n\ntest worker filesystem boundary\n"
		}
		briefAncestorRenameSucceeded, briefAPIPinnedWriteSucceeded, briefRedirectedWriteSucceeded = runBriefAncestorReplacementProbe(job.TargetDir, job.RunID, job.Task, briefs)
		var approvalsAfterProbe []approval.PendingApproval
		workerAPIAfterBriefAncestorProbe = port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvalsAfterProbe) == nil && len(approvalsAfterProbe) == 1 && approvalsAfterProbe[0].ID == sandboxBriefAncestorProbeApprovalID
		visibleAfterProbe, visibleErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
		writeAfterProbeErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-after-brief-ancestor-probe.txt"), []byte("workspace-remains-available"), 0600)
		workspaceAfterBriefAncestorProbe = visibleErr == nil && string(visibleAfterProbe) == "target-visible" && writeAfterProbeErr == nil
		var eventsAfterProbe []evidence.Event
		workspaceAfterEventReplacementProbe := true
		workerAPIAfterEventProbe = port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvalsAfterProbe) == nil && len(approvalsAfterProbe) == 1 && approvalsAfterProbe[0].ID == sandboxBriefAncestorProbeApprovalID
		if eventLogAPIReadAppendSucceeded {
			eventsAfterProbe, _ = eventLog.Read(job.RunID)
			eventLogStateReplacementAPIPinned, eventLogTeamReplacementAPIPinned = runEventAncestorReplacementProbe(job.TargetDir, job.RunID, eventLog)
			verifiedAfterReplacement, verifyErr := eventLog.Read(job.RunID)
			eventLogAPIReadAppendSucceeded = verifyErr == nil && len(verifiedAfterReplacement) == 4 && len(eventsAfterProbe) == 2 &&
				verifiedAfterReplacement[0].SHA256 == eventsAfterProbe[0].SHA256 && verifiedAfterReplacement[1].SHA256 == eventsAfterProbe[1].SHA256
			workerAPIAfterEventProbe = port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvalsAfterProbe) == nil && len(approvalsAfterProbe) == 1 && approvalsAfterProbe[0].ID == sandboxBriefAncestorProbeApprovalID
			visibleAfterEventReplacement, visibleReplacementErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
			writeAfterEventReplacementErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-after-event-replacement.txt"), []byte("workspace-remains-available"), 0600)
			workspaceAfterEventReplacementProbe = visibleReplacementErr == nil && string(visibleAfterEventReplacement) == "target-visible" && writeAfterEventReplacementErr == nil
		}
		visibleAfterEventProbe, visibleEventErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
		writeAfterEventProbeErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-after-event-probe.txt"), []byte("workspace-remains-available"), 0600)
		workspaceAfterEventProbe = workspaceAfterEventReplacementProbe && visibleEventErr == nil && string(visibleAfterEventProbe) == "target-visible" && writeAfterEventProbeErr == nil
	}
	canDial := func(address string) bool {
		conn, dialErr := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	openAIProxyReachable, deniedOtherHost, deniedOtherPort := runOpenAIEgressProbe(t)
	egressSocketStillUsable := openAIProxyReachable
	report := sandboxProbeReport{
		DatabaseReadable:                       dbErr == nil && strings.Contains(string(dbData), "controller-db-secret"),
		WALReadable:                            walErr == nil && strings.Contains(string(walData), "controller-wal-secret"),
		SHMReadable:                            shmErr == nil && strings.Contains(string(shmData), "controller-shm-secret"),
		JournalReadable:                        journalErr == nil && strings.Contains(string(journalData), "controller-journal-secret"),
		LifecycleReadable:                      lifecycleErr == nil && strings.Contains(string(lifecycleData), "lifecycle-secret"),
		EventAuthorityProofReadable:            eventAuthorityProofErr == nil && strings.Contains(string(eventAuthorityProof), "event-authority-secret"),
		LegacyApprovalReadable:                 approvalErr == nil && strings.Contains(string(approvalData), "legacy-approval-secret"),
		CandidateMetadataReadable:              candidateErr == nil && strings.Contains(string(candidateData), "candidate-metadata-secret"),
		CandidateEvidenceReadable:              candidateEvidenceErr == nil && len(candidateEvidenceData) > 0,
		CandidateEvidenceDirectWriteSucceeded:  candidateEvidenceDirectWriteErr == nil,
		CandidateEvidenceAPIWriteReadSucceeded: candidateEvidenceAPIWriteReadSucceeded,
		AttemptManifestReadable:                attemptManifestErr == nil && len(attemptManifestData) > 0,
		AttemptManifestDirectWriteSucceeded:    attemptManifestDirectWriteErr == nil,
		AttemptManifestAPIWriteReadSucceeded:   attemptManifestAPIWriteReadSucceeded,
		UsageStateReadable:                     usageErr == nil && strings.Contains(string(usageData), "usage-envelope-secret"),
		DeliveryStateReadable:                  deliveryErr == nil && len(deliveryData) > 0,
		DeliveryDirectWriteSucceeded:           deliveryDirectWriteErr == nil,
		DeliveryReceiptReadable:                deliveryReceiptErr == nil && len(deliveryReceiptData) > 0,
		DeliveryReceiptDirectWriteSucceeded:    deliveryReceiptDirectWriteErr == nil,
		DeliveryReceiptAPIWriteSucceeded:       deliveryReceiptAPIWriteSucceeded,
		DeliveryAPIWriteSucceeded:              deliveryAPIWriteSucceeded,
		AttestationStateReadable:               attestationErr == nil && len(attestationData) > 0,
		AttestationDirectWriteSucceeded:        attestationDirectWriteErr == nil,
		AttestationAPIWriteSucceeded:           attestationAPIWriteSucceeded,
		ContainmentStateReadable:               containmentErr == nil && len(containmentData) > 0,
		ContainmentDirectWriteSucceeded:        containmentDirectWriteErr == nil,
		ContainmentAPIWriteSucceeded:           containmentAPIWriteSucceeded,
		UsageAPIWriteSucceeded:                 usageAPIWriteSucceeded,
		BriefSourceReadable:                    briefErr == nil,
		BriefSourceWriteSucceeded:              briefWriteErr == nil,
		OtherRunBriefReadable:                  otherRunBriefErr == nil,
		OtherRunBriefWriteSucceeded:            otherRunBriefWriteErr == nil,
		BriefAPIListReadSucceeded:              briefAPIListReadSucceeded,
		BriefAncestorRenameSucceeded:           briefAncestorRenameSucceeded,
		BriefAPIPinnedWriteSucceeded:           briefAPIPinnedWriteSucceeded,
		BriefRedirectedWriteSucceeded:          briefRedirectedWriteSucceeded,
		WorkerAPIAfterBriefAncestorProbe:       workerAPIAfterBriefAncestorProbe,
		WorkspaceAfterBriefAncestorProbe:       workspaceAfterBriefAncestorProbe,
		RunDirectoryAbsentBeforeEvidenceStart:  runDirectoryAbsentBeforeEvidenceStart,
		EvidenceStartSucceeded:                 evidenceStartSucceeded,
		EventLogDirectReadable:                 eventLogDirectReadable,
		EventLogDirectWriteSucceeded:           eventLogDirectWriteSucceeded,
		EventLogAPIReadAppendSucceeded:         eventLogAPIReadAppendSucceeded,
		EventLogStateReplacementAPIPinned:      eventLogStateReplacementAPIPinned,
		EventLogTeamReplacementAPIPinned:       eventLogTeamReplacementAPIPinned,
		CapabilityParentEnvironmentReadable:    capabilityParentEnvironmentReadable,
		APISocketReplacementSucceeded:          apiSocketReplacementSucceeded,
		EgressSocketReplacementSucceeded:       egressSocketReplacementSucceeded,
		APISocketStillUsable:                   apiSocketStillUsable,
		EgressSocketStillUsable:                egressSocketStillUsable,
		WorkerAPIAfterEventProbe:               workerAPIAfterEventProbe,
		WorkspaceAfterEventProbe:               workspaceAfterEventProbe,
		WorktreeReadable:                       worktreeErr == nil && string(worktreeData) == "worktree-visible",
		TargetReadable:                         targetErr == nil && string(targetData) == "target-visible",
		TargetWritable:                         writeErr == nil,
		AgentRegistryWritable:                  agentWriteErr == nil,
		ControllerAPIReachable:                 apiReachable,
		AdminControlPlaneCallRejected:          adminControlPlaneCallRejected,
		AuthSecretAbsent:                       os.Getenv("AI_TEAM_AUTH_SECRET") == "",
		SigningKeyAbsent:                       os.Getenv("AI_TEAM_SIGNING_KEY") == "",
		DatabasePasswordAbsent:                 os.Getenv("AI_TEAM_DB_PASSWORD") == "",
		HostingWriteTokenAbsent:                os.Getenv("AI_TEAM_HOSTING_WRITE_TOKEN") == "",
		HostTCPReachable:                       canDial(value("--probe-host-tcp")),
		OutboundTCPReachable:                   canDial("1.1.1.1:443"),
		OpenAIProxyReachable:                   openAIProxyReachable,
		OpenAIDeniedOtherHost:                  deniedOtherHost,
		OpenAIDeniedOtherPort:                  deniedOtherPort,
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(value("--probe-output"), encoded, 0600); err != nil {
		t.Fatalf("write probe report: %v", err)
	}
	result, err := json.Marshal(Result{SchemaVersion: ResultSchemaVersion, RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, Outcome: OutcomeCompleted})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", ResultPrefix, result)
}

func probeChildCanReadParentCapabilities(t *testing.T) bool {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestBubblewrapParentEnvironmentReaderHelper$")
	command.Env = []string{"AI_TEAM_PARENT_ENV_PROBE=1"}
	err := command.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("parent procfs reader probe did not report the expected denied read: %v", err)
	}
	return false
}

func TestBubblewrapParentEnvironmentReaderHelper(t *testing.T) {
	if os.Getenv("AI_TEAM_PARENT_ENV_PROBE") != "1" {
		return
	}
	data, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(os.Getppid()), "environ"))
	if err != nil {
		os.Exit(17)
	}
	if bytes.Contains(data, []byte(WorkerAPITokenEnv+"=")) || bytes.Contains(data, []byte(OpenAIEgressTokenEnv+"=")) {
		os.Exit(0)
	}
	// A readable parent with no capability still means the probe was not
	// exercising PR_SET_DUMPABLE against the worker's inherited environment.
	os.Exit(18)
}

func probeSocketReplacement(path string) bool {
	if path == "" {
		return true
	}
	if _, err := os.Lstat(path); err != nil {
		return true
	}
	removed := os.Remove(path) == nil
	listener, err := net.Listen("unix", path)
	if err == nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return true
	}
	return removed
}

func runBriefAncestorReplacementProbe(target, runID, task string, briefs pipeline.BriefStore) (ancestorRenamed, pinnedWrite, redirectedWrite bool) {
	teamRoot := filepath.Join(target, ".ai-team")
	stateRoot := filepath.Join(teamRoot, "state")
	canonicalBriefRoot := filepath.Join(stateRoot, "briefs")
	canonicalBriefInfo, err := os.Stat(canonicalBriefRoot)
	if err != nil {
		return false, false, false
	}
	for _, ancestor := range []string{stateRoot, teamRoot} {
		backup := ancestor + "-brief-redirect-probe"
		if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		ancestorInfo, err := os.Stat(ancestor)
		if err != nil || !ancestorInfo.IsDir() {
			continue
		}
		if err := os.Rename(ancestor, backup); err != nil {
			continue
		}
		ancestorRenamed = true
		redirectRunRoot := filepath.Join(target, ".ai-team", "state", "briefs", runID)
		setupErr := seedRedirectBriefProbeFiles(redirectRunRoot, task)
		if setupErr != nil {
			_ = os.RemoveAll(ancestor)
			_ = os.Rename(backup, ancestor)
			return ancestorRenamed, false, false
		}
		type appendResult struct {
			document pipeline.BriefDocument
			err      error
		}
		result := make(chan appendResult, 1)
		go func() {
			document, err := briefs.AppendClarification(runID, sandboxBriefAncestorProbeApprovalID,
				pipeline.ClarificationProvenance{Stage: "questioner", ActorID: "qa@example.com", ActorRole: "qa"},
				sandboxBriefAncestorProbeQuestions, sandboxBriefAncestorProbeAnswer)
			result <- appendResult{document: document, err: err}
		}()
		var appended appendResult
		select {
		case appended = <-result:
		case <-time.After(10 * time.Second):
			appended.err = errors.New("brief API timed out during ancestor replacement")
		}
		redirectedPath := filepath.Join(redirectRunRoot, "0002-answer-ancestor-redirection-probe.md")
		if _, err := os.Lstat(redirectedPath); err == nil {
			redirectedWrite = true
		}
		removeErr := os.RemoveAll(ancestor)
		restoreErr := os.Rename(backup, ancestor)
		if removeErr != nil || restoreErr != nil || appended.err != nil {
			return ancestorRenamed, false, redirectedWrite
		}
		briefInfoAfter, statErr := os.Stat(canonicalBriefRoot)
		if statErr != nil || !os.SameFile(canonicalBriefInfo, briefInfoAfter) {
			return ancestorRenamed, false, redirectedWrite
		}
		versions, listErr := briefs.List(runID)
		if listErr != nil || len(versions) != 2 || versions[1].ID != appended.document.Version.ID {
			return ancestorRenamed, false, redirectedWrite
		}
		loaded, readErr := briefs.Read(runID, appended.document.Version.ID)
		pinnedWrite = readErr == nil && string(loaded.Content) == string(appended.document.Content)
		return ancestorRenamed, pinnedWrite, redirectedWrite
	}
	return false, false, false
}

// runEventAncestorReplacementProbe changes both pathname ancestors from inside
// the Linux child namespace, then reads/appends through the controller API
// while a decoy events.jsonl is visible at the replacement path. The API must
// stay attached to the pre-spawn event/reservation descriptors.
func runEventAncestorReplacementProbe(target, runID string, eventLog evidence.EventLog) (statePinned, teamPinned bool) {
	teamRoot := filepath.Join(target, ".ai-team")
	stateRoot := filepath.Join(teamRoot, "state")
	for _, probe := range []struct {
		ancestor string
		backup   string
		decoy    string
		result   *bool
	}{
		{ancestor: stateRoot, backup: stateRoot + "-event-redirect-probe", decoy: stateRoot, result: &statePinned},
		{ancestor: teamRoot, backup: teamRoot + "-event-redirect-probe", decoy: teamRoot, result: &teamPinned},
	} {
		if _, err := os.Lstat(probe.backup); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(probe.ancestor, probe.backup); err != nil {
			continue
		}
		decoyRunDir := filepath.Join(probe.decoy, "state", "events", runID)
		if err := os.MkdirAll(decoyRunDir, 0700); err != nil {
			_ = os.RemoveAll(probe.decoy)
			_ = os.Rename(probe.backup, probe.ancestor)
			continue
		}
		decoyBytes := []byte("decoy-event-journal-must-remain-unchanged")
		decoyPath := filepath.Join(decoyRunDir, "events.jsonl")
		if err := os.WriteFile(decoyPath, decoyBytes, 0600); err != nil {
			_ = os.RemoveAll(probe.decoy)
			_ = os.Rename(probe.backup, probe.ancestor)
			continue
		}
		before, readErr := eventLog.Read(runID)
		appendOK := false
		if readErr == nil && len(before) > 0 {
			appended, appendErr := eventLog.Append(runID, evidence.Event{Type: "run_paused", Timestamp: time.Now().UTC(), Data: map[string]any{
				"ancestor_replacement_probe": filepath.Base(probe.ancestor),
			}}, uint64(len(before)), before[len(before)-1].SHA256)
			if appendErr == nil {
				after, afterErr := eventLog.Read(runID)
				appendOK = afterErr == nil && len(after) == len(before)+1 && after[len(after)-1].SHA256 == appended.SHA256 &&
					after[0].SHA256 == before[0].SHA256
			}
		}
		currentDecoy, decoyErr := os.ReadFile(decoyPath)
		decoyUntouched := decoyErr == nil && bytes.Equal(currentDecoy, decoyBytes)
		cleanupErr := os.RemoveAll(probe.decoy)
		restoreErr := os.Rename(probe.backup, probe.ancestor)
		if cleanupErr == nil && restoreErr == nil {
			afterRestore, restoreReadErr := eventLog.Read(runID)
			*probe.result = appendOK && decoyUntouched && restoreReadErr == nil && len(afterRestore) == len(before)+1
		}
	}
	return statePinned, teamPinned
}

func seedRedirectBriefProbeFiles(root, task string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	content := []byte("# Исходное намерение\n\n" + strings.TrimSpace(task) + "\n")
	if err := os.WriteFile(filepath.Join(root, "0001-intention.md"), content, 0o444); err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	metadata, err := json.MarshalIndent(struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}{ID: "brief-" + hash[:16], Path: "brief/0001-intention.md", SHA256: hash}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "0001-intention.json"), append(metadata, '\n'), 0o444)
}

func isExpectedAdminControlPlaneRejection(err error) bool {
	var responseErr workerAPIResponseError
	return errors.As(err, &responseErr) &&
		responseErr.status == http.StatusBadRequest &&
		responseErr.message == `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`
}

func TestExpectedAdminControlPlaneRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "expected worker API denial",
			err:  workerAPIResponseError{status: http.StatusBadRequest, message: `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`},
			want: true,
		},
		{name: "transport error", err: errors.New("connection reset")},
		{name: "authentication error", err: workerAPIResponseError{status: http.StatusUnauthorized, message: "worker API unauthorized"}},
		{name: "wrong response status", err: workerAPIResponseError{status: http.StatusInternalServerError, message: `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`}},
		{name: "wrong rejection message", err: workerAPIResponseError{status: http.StatusBadRequest, message: "worker API admin.control_plane: invalid request"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExpectedAdminControlPlaneRejection(tt.err); got != tt.want {
				t.Fatalf("isExpectedAdminControlPlaneRejection(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func argsAfterDoubleDash(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}
