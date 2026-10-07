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
	agentDir := filepath.Join(target, ".ai-team", "agents")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "role.md")
	if err := os.WriteFile(agentPath, []byte("agent-definition"), 0600); err != nil {
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
		WithAgentRegistryPaths([]string{agentDir}),
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
	if !report.TargetReadable || !report.TargetWritable || report.AgentRegistryWritable {
		t.Fatalf("unexpected workspace or agent-registry access: %+v", report)
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

func TestBubblewrapPathAndFileValidationFailsClosed(t *testing.T) {
	t.Run("missing bubblewrap", func(t *testing.T) {
		path := t.TempDir()
		t.Setenv("PATH", path)
		if err := checkBubblewrapAvailable(); err == nil || !strings.Contains(err.Error(), "requires bubblewrap") {
			t.Fatalf("missing bubblewrap must be rejected, got %v", err)
		}
	})
	t.Run("command builder rejects root before runtime lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), string(filepath.Separator), "unused.db", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "filesystem root") {
			t.Fatalf("root workspace must fail before looking up bwrap, got %v", err)
		}
	})
	t.Run("command builder reports missing runtime", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		target := makeBubblewrapTarget(t)
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, filepath.Join(target, "controller.db"), nil, []string{"HOME=/tmp", "TMPDIR=/tmp"})
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
				_, err := bubblewrapWorkerCommand(context.Background(), worker, tc.target, tc.dbPath, tc.agentPaths, tc.env)
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
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, nil, env)
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
	_, err = bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, nil, env)
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
	AgentRegistryWritable  bool `json:"agent_registry_writable"`
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
	agentWriteErr := os.WriteFile(filepath.Join(job.TargetDir, ".ai-team", "agents", "role.md"), []byte("modified"), 0600)
	report := sandboxProbeReport{
		DatabaseReadable:       dbErr == nil && strings.Contains(string(dbData), "controller-db-secret"),
		WALReadable:            walErr == nil && strings.Contains(string(walData), "controller-wal-secret"),
		SHMReadable:            shmErr == nil && strings.Contains(string(shmData), "controller-shm-secret"),
		JournalReadable:        journalErr == nil && strings.Contains(string(journalData), "controller-journal-secret"),
		LifecycleReadable:      lifecycleErr == nil && strings.Contains(string(lifecycleData), "lifecycle-secret"),
		LegacyApprovalReadable: approvalErr == nil && strings.Contains(string(approvalData), "legacy-approval-secret"),
		TargetReadable:         targetErr == nil && string(targetData) == "target-visible",
		TargetWritable:         writeErr == nil,
		AgentRegistryWritable:  agentWriteErr == nil,
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
