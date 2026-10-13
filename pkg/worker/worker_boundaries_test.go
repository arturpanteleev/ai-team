package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

func TestNewProcessEngineRejectsMissingTargetAndEmptyAgentRegistryPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-target")
	if _, err := NewProcessEngine([]string{"ai-team"}, missing, filepath.Join(t.TempDir(), "db.sqlite")); err == nil {
		t.Fatal("worker engine accepted a target that cannot be canonicalized")
	}
	target := t.TempDir()
	_, err := NewProcessEngine([]string{"ai-team"}, target, filepath.Join(target, "db.sqlite"),
		WithAgentRegistryPaths([]string{filepath.Join(target, "agents"), ""}))
	if err == nil || !strings.Contains(err.Error(), "agent registry") {
		t.Fatalf("empty agent registry path error = %v, want rejection", err)
	}
}

func TestProcessEngineExecuteFailsClosedBeforeSpawning(t *testing.T) {
	target := t.TempDir()
	// The worker binary does not exist: reaching command.Run would surface a
	// spawn error instead of the validation errors asserted below.
	argv := []string{filepath.Join(target, "never-spawned")}

	sandboxed := &ProcessEngine{argv: argv, target: target, dbPath: filepath.Join(target, "db.sqlite"), bubblewrap: true}
	if _, err := sandboxed.Execute(context.Background(), Job{}); err == nil || !strings.Contains(err.Error(), "requires the controller API") {
		t.Fatalf("bubblewrap worker without controller API error = %v, want fail-closed", err)
	}

	engine, err := NewProcessEngine(argv, target, filepath.Join(target, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(context.Background(), Job{}); err == nil {
		t.Fatal("worker engine executed a job that is not a valid queued job")
	}
}

func TestReadQuestionAnswerMountFileRejectsMissingSymlinkAndDirectory(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "answer.md")
	if err := os.WriteFile(regular, []byte("answer"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readQuestionAnswerMountFile(filepath.Join(dir, "missing.md"), maxQuestionAnswerMountBytes); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing input error = %v, want ErrNotExist", err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"symlink": link, "directory": dir} {
		if _, _, err := readQuestionAnswerMountFile(path, maxQuestionAnswerMountBytes); err == nil || !strings.Contains(err.Error(), "bounded regular file without symlink") {
			t.Fatalf("%s input error = %v, want regular-file rejection", name, err)
		}
	}
	if _, _, err := readQuestionAnswerMountFile(regular, 3); err == nil || !strings.Contains(err.Error(), "bounded regular file") {
		t.Fatalf("oversized input error = %v, want size rejection", err)
	}
}

func TestPrepareQuestionAnswerMountpointFailsWhenParentIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	parent := filepath.Join(t.TempDir(), "inputs")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	created, info, err := prepareQuestionAnswerMountpoint(filepath.Join(parent, "answer.md"), []byte("answer"))
	if err == nil || created || info != nil {
		t.Fatalf("mountpoint in read-only parent: created=%v info=%v err=%v; want failure", created, info, err)
	}
}

func TestCreateControllerWorkerSocketDirRefusesWritableBindCoveringEveryParent(t *testing.T) {
	// A worker-writable bind at the filesystem root covers every candidate
	// parent, so no capability socket directory is safe.
	dir, err := createControllerWorkerSocketDir(string(filepath.Separator), "", "")
	if err == nil || dir != "" || !strings.Contains(err.Error(), "outside worker-writable binds") {
		t.Fatalf("socket dir = %q err = %v; want refusal", dir, err)
	}
	if isWithinPath("/a", "relative/path") {
		t.Fatal("relative path must not be treated as within an absolute parent")
	}
	if isWithinPath("", "/a") || isWithinPath("/a", "") {
		t.Fatal("empty paths must never be within each other")
	}
}

type stubEventLog struct{ events []evidence.Event }

func (s *stubEventLog) Read(string) ([]evidence.Event, error) { return s.events, nil }
func (s *stubEventLog) ReadBytes(string) ([]byte, error)      { return nil, nil }
func (s *stubEventLog) Append(string, evidence.Event, uint64, string) (evidence.Event, error) {
	return evidence.Event{}, errors.New("stub event log is read-only")
}

func TestWorkerAPIEventLogAppendGates(t *testing.T) {
	const runID = "event-gates"
	noTransport := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart}}
	if _, err := noTransport.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "x"}}); err == nil || !strings.Contains(err.Error(), "require bubblewrap Unix transport") {
		t.Fatalf("event append without Unix transport error = %v", err)
	}
	cancel := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel, TargetDir: t.TempDir()},
		usageAllowed: true, eventLogs: &stubEventLog{}}
	if _, err := cancel.dispatch("event_log.description_missing", workerAPICall{RunID: runID, Event: evidence.Event{Type: "description_missing"}}); err == nil || !strings.Contains(err.Error(), "controller description event is not allowed") {
		t.Fatalf("controller description event during cancel error = %v", err)
	}
}

func TestWorkerAPIHumanSubmissionManifestRejectsMismatchedDecision(t *testing.T) {
	const runID, approvalID = "human-manifest-decision", "approval-decision"
	payload, err := json.Marshal(approval.InputPayload{Kind: string(approval.KindInput), StageID: "writer", Result: "md", OutputName: "result"})
	if err != nil {
		t.Fatal(err)
	}
	decision := approval.Decision{ApprovalID: approvalID, ActorID: "dev-1", ActorRole: "developer", Action: "reject",
		Comment: "no", SubmissionVersion: 1, ContentSHA256: strings.Repeat("a", 64)}
	value := approval.PendingApproval{Kind: approval.KindInput, RunID: runID, ID: approvalID, FromStage: "writer", ToStage: "writer",
		Trigger: "human_input", Actions: []string{"submit", "reject"}, Status: approval.StatusResolved, ResolvedAction: "reject",
		Payload: payload, Decisions: []approval.Decision{decision}}
	server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: t.TempDir()},
		approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
	manifest := evidence.AttemptManifest{RunID: runID, AttemptID: "attempt-writer-1", Stage: "writer", Executor: "human",
		ActorID: "dev-1", ActorRole: "developer", HumanInputApprovalID: approvalID, HumanSubmissionVersion: 1,
		HumanSubmissionSHA256: decision.ContentSHA256, HumanSubmissionResult: "md"}
	if err := server.validateHumanSubmissionManifest(manifest); err == nil || !strings.Contains(err.Error(), "unsupported resolved action") {
		t.Fatalf("typed submission bound to a reject decision error = %v", err)
	}

	manifest.ActorID = "someone-else"
	if err := server.validateHumanSubmissionManifest(manifest); err == nil || !strings.Contains(err.Error(), "actor does not match") {
		t.Fatalf("foreign actor manifest error = %v", err)
	}

	unbound := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
	if err := unbound.validateReservedHumanSubmissionManifest(manifest); err == nil || !strings.Contains(err.Error(), "requires its controller approval") {
		t.Fatalf("reserved manifest without approval store error = %v", err)
	}
}

// serveWorkerAPI must close the listener and unlink its capability socket on
// every admission failure, and a Recover dispatch must not proceed when the
// prior run's legacy journal cannot be migrated into the controller store.
func TestServeWorkerAPIAdmissionFailuresCloseListenerAndSocket(t *testing.T) {
	nonce := func() *strings.Reader { return strings.NewReader(strings.Repeat("n", 32)) }
	serve := func(t *testing.T, job Job) error {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(t.TempDir(), "controller-api.sock")
		if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		server, serveErr := serveWorkerAPI(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, listener, socketPath, nonce(), "task")
		if serveErr == nil {
			server.close()
			return nil
		}
		if _, acceptErr := listener.Accept(); !errors.Is(acceptErr, net.ErrClosed) {
			t.Fatalf("listener left open after admission failure: %v", acceptErr)
		}
		if _, statErr := os.Lstat(socketPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("capability socket path left behind after admission failure: %v", statErr)
		}
		return serveErr
	}

	t.Run("missing target", func(t *testing.T) {
		err := serve(t, Job{RunID: "valid-run", Operation: OperationStart, TargetDir: filepath.Join(t.TempDir(), "missing")})
		if err == nil || !strings.Contains(err.Error(), "canonicalize worker API attestation target") {
			t.Fatalf("missing target error = %v", err)
		}
	})
	t.Run("recover with a corrupt legacy journal", func(t *testing.T) {
		target := t.TempDir()
		runDir := filepath.Join(target, ".ai-team", "runs", "recover-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte("not a json event\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := serve(t, Job{RunID: "recover-run", Operation: OperationRecover, TargetDir: target})
		if err == nil || !strings.Contains(err.Error(), "prepare controller event log") {
			t.Fatalf("recover without a migratable journal error = %v", err)
		}
	})
}

func TestWorkerAPIHumanSubmissionRejectsActionOutsideConfiguredResult(t *testing.T) {
	const runID, approvalID = "human-manifest-action", "approval-action"
	payload, err := json.Marshal(approval.InputPayload{Kind: string(approval.KindInput), StageID: "writer", Result: "md", OutputName: "result"})
	if err != nil {
		t.Fatal(err)
	}
	decision := approval.Decision{ApprovalID: approvalID, ActorID: "dev-1", ActorRole: "developer", Action: "approve", Comment: "ok"}
	value := approval.PendingApproval{Kind: approval.KindInput, RunID: runID, ID: approvalID, FromStage: "writer", ToStage: "writer",
		Trigger: "human_input", Actions: []string{"submit", "approve"}, Status: approval.StatusResolved, ResolvedAction: "approve",
		Payload: payload, Decisions: []approval.Decision{decision}}
	server := &workerAPIServer{scope: workerAPIScope{RunID: runID},
		approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
	manifest := evidence.AttemptManifest{RunID: runID, AttemptID: "attempt-writer-1", Stage: "writer", Executor: "human",
		ActorID: "dev-1", ActorRole: "developer", HumanInputApprovalID: approvalID, HumanSubmissionVersion: 1, HumanSubmissionResult: "md"}
	if err := server.validateHumanSubmissionManifest(manifest); err == nil || !strings.Contains(err.Error(), "action does not match its configured result") {
		t.Fatalf("approve decision for an md input error = %v", err)
	}
}

// A terminal record that claims a stage-delivered commit must be backed by a
// verifiable controller stage receipt; otherwise nothing is persisted.
func TestWorkerAPITerminalRecordRejectsUnbackedStageDeliveryReceipt(t *testing.T) {
	const runID = "terminal-stage-receipt"
	target := t.TempDir()
	server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target},
		usageAllowed: true, eventLogs: &stubEventLog{}}
	record := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: runID, Feature: "feat",
		PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Now().UTC(),
		StageAttemptID: "attempt-delivery-1", StageAttemptManifestSHA256: strings.Repeat("d", 64),
		StageDeliveryReceiptSHA256: strings.Repeat("e", 64), AttestationSHA256: strings.Repeat("f", 64),
		RuntimeIdentity: strings.Repeat("b", 64)}
	if err := record.Validate(); err != nil {
		t.Fatalf("fixture must be a structurally valid record: %v", err)
	}
	if _, err := server.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: record}); err == nil ||
		!strings.Contains(err.Error(), "validate controller stage delivery terminal record") {
		t.Fatalf("unbacked stage receipt error = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(target, ".ai-team", "state", "delivery")); err == nil && len(entries) != 0 {
		t.Fatalf("terminal record persisted despite rejected stage receipt: %v", entries)
	}
}
