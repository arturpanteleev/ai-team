//go:build linux

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestBubblewrapWorkerCannotReadControllerStateAndCanUseTarget(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("Linux CI must install bubblewrap before running worker sandbox tests:", err)
	}
	target := t.TempDir()
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
	approvalDir := filepath.Join(target, ".ai-team", "state", "approvals")
	if err := os.MkdirAll(approvalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approvalDir, "pending.json"), []byte("legacy-approval-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "visible.txt"), []byte("target-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(target, "sandbox-probe.json")
	t.Setenv("AI_TEAM_BUBBLEWRAP_PROBE", "1")
	allowWorkerTestEnvironment(t, "AI_TEAM_BUBBLEWRAP_PROBE")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestBubblewrapWorkerProbeHelper$", "--", "--probe-db", dbPath,
			"--probe-wal", dbPath + "-wal", "--probe-shm", dbPath + "-shm", "--probe-journal", dbPath + "-journal",
			"--probe-output", probePath},
		target, dbPath,
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
		WithLinuxBubblewrapIsolation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "sandbox-probe", Feature: "probe", TaskDesc: "test worker filesystem boundary", TargetDir: target,
	}); err != nil {
		t.Fatalf("bubblewrap worker invocation failed (runtime must fail closed): %v", err)
	}
	data, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	var report sandboxProbeReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid probe report %q: %v", data, err)
	}
	if report.DatabaseReadable || report.WALReadable || report.SHMReadable || report.JournalReadable || report.LifecycleReadable || report.LegacyApprovalReadable {
		t.Fatalf("controller-owned state visible inside worker: %+v", report)
	}
	if !report.TargetReadable || !report.TargetWritable {
		t.Fatalf("worker lost required target workspace access: %+v", report)
	}
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
	DatabaseReadable       bool `json:"database_readable"`
	WALReadable            bool `json:"wal_readable"`
	SHMReadable            bool `json:"shm_readable"`
	JournalReadable        bool `json:"journal_readable"`
	LifecycleReadable      bool `json:"lifecycle_readable"`
	LegacyApprovalReadable bool `json:"legacy_approval_readable"`
	TargetReadable         bool `json:"target_readable"`
	TargetWritable         bool `json:"target_writable"`
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
	dbData, dbErr := os.ReadFile(value("--probe-db"))
	walData, walErr := os.ReadFile(value("--probe-wal"))
	shmData, shmErr := os.ReadFile(value("--probe-shm"))
	journalData, journalErr := os.ReadFile(value("--probe-journal"))
	lifecycleData, lifecycleErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "runs", "controller-state.json"))
	approvalData, approvalErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "approvals", "pending.json"))
	targetData, targetErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
	writeErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-write.txt"), []byte("worker-write"), 0600)
	report := sandboxProbeReport{
		DatabaseReadable:       dbErr == nil && strings.Contains(string(dbData), "controller-db-secret"),
		WALReadable:            walErr == nil && strings.Contains(string(walData), "controller-wal-secret"),
		SHMReadable:            shmErr == nil && strings.Contains(string(shmData), "controller-shm-secret"),
		JournalReadable:        journalErr == nil && strings.Contains(string(journalData), "controller-journal-secret"),
		LifecycleReadable:      lifecycleErr == nil && strings.Contains(string(lifecycleData), "lifecycle-secret"),
		LegacyApprovalReadable: approvalErr == nil && strings.Contains(string(approvalData), "legacy-approval-secret"),
		TargetReadable:         targetErr == nil && string(targetData) == "target-visible",
		TargetWritable:         writeErr == nil,
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

func argsAfterDoubleDash(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}
