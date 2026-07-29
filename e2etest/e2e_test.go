package e2etest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/scheduler"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "ai-team")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ai-team")
	cmd.Dir = findModuleRoot()
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build ai-team: %v", err)
	}
	return binPath
}

func findModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// runAI запускает бинарник; stdin — /dev/null (неинтерактивный режим).
// Возвращает exit-код и combined output.
func runAI(t *testing.T, binPath, dir string, envs []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, envs...)
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	t.Logf("ai-team %v → exit %d", args, code)
	return code, out.String()
}

func setupMock(t *testing.T) string {
	t.Helper()
	projectRoot := findModuleRoot()
	mockSrc := filepath.Join(projectRoot, "e2etest", "mock-opencode.sh")
	mockDir := filepath.Join(t.TempDir(), "mockbin")
	if err := os.MkdirAll(mockDir, 0755); err != nil {
		t.Fatalf("mkdir mockbin: %v", err)
	}
	mockDst := filepath.Join(mockDir, "opencode")
	if err := os.Symlink(mockSrc, mockDst); err != nil {
		t.Fatalf("symlink opencode mock: %v", err)
	}
	ghPath := filepath.Join(mockDir, "gh")
	ghMarker := filepath.Join(mockDir, "gh-pr-created")
	ghScript := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo "authenticated"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  if [ ! -f "` + ghMarker + `" ]; then
    exit 1
  fi
  oid=$(git rev-parse HEAD)
  printf '{"url":"https://example.test/pr/e2e","state":"OPEN","baseRefName":"main","headRefName":"%s","headRefOid":"%s"}\n' "$3" "$oid"
  exit 0
fi
touch "` + ghMarker + `"
echo https://example.test/pr/e2e
`
	if err := os.WriteFile(ghPath, []byte(ghScript), 0755); err != nil {
		t.Fatalf("write gh mock: %v", err)
	}
	return fmt.Sprintf("PATH=%s%c%s", mockDir, os.PathListSeparator, os.Getenv("PATH"))
}

func setupDeliveryGit(t *testing.T, dir string) {
	t.Helper()
	runGit := func(workDir string, args ...string) {
		command := exec.Command("git", args...)
		command.Dir = workDir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit(dir, "init", "-b", "main")
	runGit(dir, "config", "user.name", "AI Team E2E")
	runGit(dir, "config", "user.email", "ai-team@example.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/e2e\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(dir, "add", "README.md", "go.mod")
	runGit(dir, "commit", "-m", "initial")
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(filepath.Dir(remote), "init", "--bare", remote)
	runGit(dir, "remote", "add", "origin", remote)
	runGit(dir, "push", "-u", "origin", "main")
}

func artifactsDir(dir, feature string, parts ...string) string {
	return filepath.Join(append([]string{dir, ".ai-team", "artifacts", feature}, parts...)...)
}

func checkFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected %s to exist", path)
	}
}

func checkAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s to NOT exist", path)
	}
}

func checkDir(t *testing.T, parts ...string) {
	t.Helper()
	path := filepath.Join(parts...)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected directory %s to exist: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", path)
	}
}

func TestE2E_SuccessfulPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	liveBranchBefore := strings.TrimSpace(runCommand(t, dir, "git", "branch", "--show-current"))
	liveHeadBefore := strings.TrimSpace(runCommand(t, dir, "git", "rev-parse", "HEAD"))

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}
	if status := strings.TrimSpace(runCommand(t, dir, "git", "status", "--porcelain")); status != "" {
		t.Fatalf("workspace должен остаться чистым после init, получено:\n%s", status)
	}
	configData, err := os.ReadFile(filepath.Join(dir, ".ai-team", "config.yaml"))
	if err != nil || !strings.Contains(string(configData), "name: go-test") {
		t.Fatalf("Go verification profile не создан: err=%v\n%s", err, configData)
	}

	checkDir(t, dir, ".ai-team")
	checkDir(t, dir, ".ai-team", "artifacts")

	code, firstOut := runAI(t, bin, dir, []string{pathEnv}, "run", "--feature", "e2e-test", "--task", "E2E test task", "--approve-gates")
	if code != 3 {
		t.Fatalf("first run must stop for exact delivery approval (exit 3), got %d:\n%s", code, firstOut)
	}
	hashMatch := regexp.MustCompile(`Plan SHA-256: ([a-f0-9]{64})`).FindStringSubmatch(firstOut)
	if len(hashMatch) != 2 {
		t.Fatalf("canonical delivery plan hash missing:\n%s", firstOut)
	}
	resumeMatch := regexp.MustCompile(`ai-team run --resume ([^ ]+) --approve-plan`).FindStringSubmatch(firstOut)
	if len(resumeMatch) != 2 {
		t.Fatalf("resume command missing:\n%s", firstOut)
	}
	code, out := runAI(t, bin, dir, []string{pathEnv}, "run", "--resume", resumeMatch[1], "--approve-gates", "--approve-plan", hashMatch[1])
	if code != 0 {
		t.Fatalf("approved delivery retry failed (%d):\n%s", code, out)
	}

	feature := "e2e-test"
	checkFile(t, artifactsDir(dir, feature, "proposal.md"))
	checkFile(t, artifactsDir(dir, feature, "specs", "product", "spec.md"))
	checkFile(t, artifactsDir(dir, feature, "design.md"))
	checkFile(t, artifactsDir(dir, feature, "tasks.md"))
	checkFile(t, artifactsDir(dir, feature, "review.md"))
	checkFile(t, artifactsDir(dir, feature, "test-report.md"))
	checkFile(t, artifactsDir(dir, feature, "verification.md"))
	checkFile(t, artifactsDir(dir, feature, "delivery-plan.json"))
	checkFile(t, artifactsDir(dir, feature, ".control", "review-candidate.json"))
	verificationCandidate := artifactsDir(dir, feature, ".control", "verification-candidate.json")
	checkFile(t, verificationCandidate)
	candidateData, err := os.ReadFile(verificationCandidate)
	if err != nil {
		t.Fatal(err)
	}
	var candidate struct {
		WorkspaceSHA256 string `json:"workspace_sha256"`
		Checks          []struct {
			Adapter         string `json:"adapter"`
			DiscoveredTests int    `json:"discovered_tests"`
			PassedTests     int    `json:"passed_tests"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(candidateData, &candidate); err != nil || len(candidate.WorkspaceSHA256) != 64 ||
		len(candidate.Checks) == 0 || candidate.Checks[0].Adapter != "go-test-json" ||
		candidate.Checks[0].DiscoveredTests == 0 || candidate.Checks[0].PassedTests == 0 {
		t.Fatalf("controller verification candidate is incomplete: %+v err=%v", candidate, err)
	}

	// Наблюдаемость: логи агентов, отчёты и запись запуска в SQLite
	checkFile(t, filepath.Join(dir, ".ai-team", "reports", feature, "index.html"))
	checkFile(t, filepath.Join(dir, ".ai-team", "web.db"))
	runs, err := os.ReadDir(filepath.Join(dir, ".ai-team", "runs"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("prepare/approve flow должен продолжить тот же immutable run: entries=%v err=%v", runs, err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, ".ai-team", "runs", "*", "logs", "*-analyst.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("immutable analyst log отсутствует: logs=%v err=%v", logs, err)
	}

	if !strings.Contains(out, "Пайплайн выполнен") {
		t.Errorf("ожидалось сообщение об успехе:\n%s", out)
	}
	if branch := strings.TrimSpace(runCommand(t, dir, "git", "branch", "--show-current")); branch != liveBranchBefore {
		t.Errorf("live checkout branch изменился: before=%q after=%q", liveBranchBefore, branch)
	}
	if head := strings.TrimSpace(runCommand(t, dir, "git", "rev-parse", "HEAD")); head != liveHeadBefore {
		t.Errorf("live checkout HEAD изменился: before=%q after=%q", liveHeadBefore, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e_implementation.go")); !os.IsNotExist(err) {
		t.Errorf("candidate source не должен появиться в live checkout: %v", err)
	}
	if committed := strings.TrimSpace(runCommand(t, dir, "git", "show", "--name-only", "--format=", "origin/ai-team/e2e-test")); committed != "e2e_implementation.go\ne2e_implementation_test.go" {
		t.Errorf("delivery должен коммитить exact run-attributed file, got %q", committed)
	}
}

func TestE2E_DisposableWorkerPersistsPendingApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	runID := "worker-e2e-run"
	job, err := json.Marshal(worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
		RunID: runID, TargetDir: dir, Feature: "worker-e2e", Task: "worker task",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin, "worker", "--target", dir, "--db", filepath.Join(dir, ".ai-team", "web.db"))
	command.Dir = dir
	command.Env = append(os.Environ(), pathEnv)
	command.Stdin = strings.NewReader(string(job))
	output, commandErr := command.CombinedOutput()
	exit := 0
	if commandErr != nil {
		if value, ok := commandErr.(*exec.ExitError); ok {
			exit = value.ExitCode()
		} else {
			exit = -1
		}
	}
	if exit != 3 {
		t.Fatalf("worker должен остановиться на human approval: exit=%d err=%v\n%s", exit, commandErr, output)
	}
	state, err := os.ReadFile(filepath.Join(dir, ".ai-team", "state", "runs", runID+".json"))
	if err != nil || !strings.Contains(string(state), `"phase": "waiting"`) {
		t.Fatalf("worker не сохранил waiting lifecycle: err=%v\n%s", err, state)
	}
	approvals, err := filepath.Glob(filepath.Join(dir, ".ai-team", "state", "approvals", runID, "*.json"))
	if err != nil || len(approvals) != 1 {
		t.Fatalf("worker не сохранил exact pending approval: %v err=%v", approvals, err)
	}
	checkFile(t, filepath.Join(dir, ".ai-team", "state", "candidates", runID+".json"))
	checkFile(t, filepath.Join(dir, ".ai-team", "web.db"))
}

func runCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

// Честный тест enforcement: rejected-режим мока ломает ТОЛЬКО reviewer.
// Пайплайн обязан остановиться сразу после reviewer — tester уже не запускается.
func TestE2E_RejectedReviewStopsPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}

	feature := "e2e-rejected"
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=rejected", "AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_MODE"},
		"run", "--feature", feature, "--task", "Test rejected", "--approve-gates")

	if code != 1 {
		t.Fatalf("ожидался exit-код 1 (негативный вердикт), got %d:\n%s", code, out)
	}
	// Пайплайн дошёл до reviewer...
	checkFile(t, artifactsDir(dir, feature, "proposal.md"))
	checkFile(t, artifactsDir(dir, feature, "design.md"))
	checkFile(t, artifactsDir(dir, feature, "review.md"))
	// ...и остановился на нём: tester и последующие не выполнялись
	checkAbsent(t, artifactsDir(dir, feature, "test-report.md"))
	checkAbsent(t, artifactsDir(dir, feature, "verification.md"))

	if !strings.Contains(out, "CHANGES_REQUESTED") {
		t.Errorf("в выводе должен быть вердикт:\n%s", out)
	}
}

// Tester FAIL останавливает пайплайн до verifier/deployer.
func TestE2E_TesterFailStopsPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}

	feature := "e2e-fail"
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=fail", "AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_MODE"},
		"run", "--feature", feature, "--task", "Test fail", "--approve-gates")

	if code != 1 {
		t.Fatalf("ожидался exit-код 1, got %d:\n%s", code, out)
	}
	checkFile(t, artifactsDir(dir, feature, "test-report.md"))
	checkAbsent(t, artifactsDir(dir, feature, "verification.md"))
}

// BLOCKED от analyst: exit-код 2, подсказка retry-from, дальнейшие этапы не идут.
func TestE2E_BlockedAnalyst(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}

	feature := "e2e-blocked"
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=blocked", "AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_MODE"},
		"run", "--feature", feature, "--task", "Test blocked", "--approve-gates")

	if code != 2 {
		t.Fatalf("ожидался exit-код 2 (BLOCKED), got %d:\n%s", code, out)
	}
	checkFile(t, artifactsDir(dir, feature, "status", "analyst.md"))
	checkAbsent(t, artifactsDir(dir, feature, "proposal.md"))
	checkAbsent(t, artifactsDir(dir, feature, "design.md"))

	if !strings.Contains(out, "retry-from analyst") {
		t.Errorf("ожидалась подсказка retry-from:\n%s", out)
	}
}

func TestE2E_InitCreatesStructure(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	runCommand(t, dir, "git", "init", "-b", "main")

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init", "--write-gitignore"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}

	cfgPath := filepath.Join(dir, ".ai-team", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.yaml should exist after init: %v", err)
	}
	cfg := string(data)
	// Init сериализует полный graph config: edge approvals и visit limits не теряются.
	for _, want := range []string{"schema_version: 4", "workflow:", "outcome: passed", "cli: opencode", "roles:", "product_owner", "max_visits:", "stage_timeout: 30m"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.yaml должен содержать %q:\n%s", want, cfg)
		}
	}

	checkDir(t, dir, ".ai-team", "artifacts", "tasks")
	checkDir(t, dir, ".ai-team", "reports")
	checkDir(t, dir, ".ai-team", "logs")
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !strings.Contains(string(gitignore), ".ai-team/") {
		t.Fatalf("--write-gitignore должен записать правило: err=%v\n%s", err, gitignore)
	}
}

// --help выводит справку и завершается с кодом 0.
func TestE2E_HelpFlag(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t)

	for _, arg := range []string{"--help", "-h", "help"} {
		code, out := runAI(t, bin, dir, nil, arg)
		if code != 0 {
			t.Errorf("%s: ожидался код 0, got %d", arg, code)
		}
		if !strings.Contains(out, "ai-team run") || !strings.Contains(out, "ai-team web") {
			t.Errorf("%s: справка неполная:\n%s", arg, out)
		}
	}
}

// Невалидное имя фичи отклоняется до создания файлов.
func TestE2E_InvalidFeatureName(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)

	runAI(t, bin, dir, []string{pathEnv}, "init")

	for _, feature := range []string{"../escape", "a/b", "a\\b"} {
		code, _ := runAI(t, bin, dir, []string{pathEnv}, "run", "--feature", feature, "--task", "x")
		if code == 0 {
			t.Errorf("фича %q должна быть отклонена", feature)
		}
	}
}

// Невалидный control-plane config отклоняется до записи task.md и до runtime.
func TestE2E_InvalidConfigDoesNotMutateTaskArtifacts(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	badConfig := "schema_version: 2\ncli: opencode\npipeline:\n  - name: analyst\n    checkpoint_afer: require_explicit\n"
	if err := os.WriteFile(filepath.Join(dir, ".ai-team", "config.yaml"), []byte(badConfig), 0644); err != nil {
		t.Fatal(err)
	}

	feature := "invalid-config"
	code, out := runAI(t, bin, dir, []string{pathEnv}, "run", "--feature", feature, "--task", "must not persist")
	if code == 0 || !strings.Contains(out, "checkpoint_afer") {
		t.Fatalf("invalid config must fail before execution: code=%d\n%s", code, out)
	}
	checkAbsent(t, filepath.Join(dir, ".ai-team", "artifacts", "tasks", feature, "task.md"))
}

func TestE2E_ResumeKeepsRunIdentityAfterProcessStop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}

	waitFile := filepath.Join(t.TempDir(), "analyst-started")
	command := exec.Command(bin, "run", "--feature", "durable-resume", "--task", "resume test", "--approve-gates")
	command.Dir = dir
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(), pathEnv,
		"MOCK_WAIT_AGENT=analyst",
		"MOCK_WAIT_FILE="+waitFile,
		"AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_WAIT_AGENT,MOCK_WAIT_FILE",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(waitFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("analyst не стартовал:\n%s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("остановленный run должен вернуть ненулевой exit")
	}

	states, err := filepath.Glob(filepath.Join(dir, ".ai-team", "state", "runs", "*.json"))
	if err != nil || len(states) != 1 {
		t.Fatalf("ожидался один lifecycle state: %v err=%v", states, err)
	}
	runID := strings.TrimSuffix(filepath.Base(states[0]), ".json")
	code, resumedOutput := runAI(t, bin, dir, []string{pathEnv}, "run", "--resume", runID, "--approve-gates")
	if code != 3 {
		t.Fatalf("resume должен дойти до отдельного delivery approval (exit 3), got %d:\n%s", code, resumedOutput)
	}
	events, err := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(events), `"type":"run_started"`) != 1 ||
		strings.Count(string(events), `"type":"run_resumed"`) != 1 {
		t.Fatalf("run identity lifecycle некорректен:\n%s", events)
	}
}

func TestE2E_WebDecisionAndResumeSameRun(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	command := exec.Command(bin, "web", "--port", port, "--dist=")
	command.Dir = dir
	command.Env = append(os.Environ(), pathEnv)
	var serverOutput strings.Builder
	command.Stdout, command.Stderr = &serverOutput, &serverOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		_ = command.Wait()
	})

	baseURL := "http://127.0.0.1:" + port
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	var csrf string
	waitUntil(t, 10*time.Second, func() bool {
		response, requestErr := client.Get(baseURL + "/api/session")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&session) != nil {
			return false
		}
		csrf = session.CSRF
		return csrf != ""
	}, func() string { return serverOutput.String() })

	post := func(path string, body any) (int, map[string]any) {
		t.Helper()
		data, _ := json.Marshal(body)
		request, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(response.Body).Decode(&result)
		return response.StatusCode, result
	}
	status, started := post("/api/runs", map[string]string{
		"feature": "web-control", "task": "web approval test",
	})
	runID, _ := started["run_id"].(string)
	if status != http.StatusAccepted || runID == "" {
		t.Fatalf("web start: status=%d body=%v\n%s", status, started, serverOutput.String())
	}
	type approvalState struct {
		ID          string `json:"id"`
		SubjectHash string `json:"subject_hash"`
	}
	readPending := func() approvalState {
		statePath := filepath.Join(dir, ".ai-team", "state", "runs", runID+".json")
		stateData, err := os.ReadFile(statePath)
		if err != nil {
			return approvalState{}
		}
		var state struct {
			Phase      string `json:"phase"`
			ApprovalID string `json:"pending_approval_id"`
		}
		if json.Unmarshal(stateData, &state) != nil || state.Phase != "waiting" {
			return approvalState{}
		}
		approvalData, err := os.ReadFile(filepath.Join(
			dir, ".ai-team", "state", "approvals", runID, state.ApprovalID+".json",
		))
		if err != nil {
			return approvalState{}
		}
		var value approvalState
		_ = json.Unmarshal(approvalData, &value)
		return value
	}
	var first approvalState
	waitUntil(t, 10*time.Second, func() bool {
		first = readPending()
		return first.ID != ""
	}, func() string { return serverOutput.String() })
	status, _ = post("/api/runs/"+runID+"/approvals/"+first.ID+"/decisions", map[string]string{
		"actor_id": "product-1", "actor_role": "product_owner",
		"action": "approve", "subject_hash": first.SubjectHash,
	})
	if status != http.StatusOK {
		t.Fatalf("web decision status=%d", status)
	}
	status, _ = post("/api/runs/"+runID+"/resume", map[string]string{})
	if status != http.StatusAccepted {
		t.Fatalf("web resume status=%d\n%s", status, serverOutput.String())
	}
	var second approvalState
	waitUntil(t, 10*time.Second, func() bool {
		second = readPending()
		return second.ID != "" && second.ID != first.ID
	}, func() string { return serverOutput.String() })

	events, err := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	analystAttempts, globErr := filepath.Glob(filepath.Join(
		dir, ".ai-team", "runs", runID, "attempts", "*-analyst",
	))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if strings.Count(string(events), `"type":"run_started"`) != 1 ||
		len(analystAttempts) != 1 ||
		!strings.Contains(string(events), `"type":"transition_selected"`) ||
		!strings.Contains(string(events), `"stage":"architect"`) {
		t.Fatalf("web resume повторил этап или сменил identity:\n%s", events)
	}
}

func TestE2E_DistributedSchedulerDispatchesAndArchivesRun(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	schedulerDB := filepath.Join(dir, ".ai-team", "scheduler.db")
	artifactRoot := filepath.Join(dir, ".ai-team", "cloud-artifacts")
	command := exec.Command(bin, "web", "--port", port, "--dist=", "--scheduler-db", schedulerDB)
	command.Dir = dir
	command.Env = append(os.Environ(), pathEnv)
	var serverOutput strings.Builder
	command.Stdout, command.Stderr = &serverOutput, &serverOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		_ = command.Wait()
	})

	baseURL := "http://127.0.0.1:" + port
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	var csrf string
	waitUntil(t, 10*time.Second, func() bool {
		response, requestErr := client.Get(baseURL + "/api/session")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&session) != nil {
			return false
		}
		csrf = session.CSRF
		return csrf != ""
	}, func() string { return serverOutput.String() })
	data, _ := json.Marshal(map[string]string{"feature": "scheduled", "task": "scheduled task"})
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/runs", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var started map[string]string
	_ = json.NewDecoder(response.Body).Decode(&started)
	response.Body.Close()
	runID := started["run_id"]
	if response.StatusCode != http.StatusAccepted || runID == "" {
		t.Fatalf("scheduler enqueue: status=%d body=%v\n%s", response.StatusCode, started, serverOutput.String())
	}

	statePath := filepath.Join(dir, ".ai-team", "state", "runs", runID+".json")
	waitUntil(t, 10*time.Second, func() bool {
		poller := exec.Command(bin, "scheduler-worker",
			"--target", dir, "--scheduler-db", schedulerDB,
			"--artifact-store", artifactRoot, "--db", filepath.Join(dir, ".ai-team", "web.db"),
			"--worker-command", bin, "--worker-id", "e2e-worker", "--once",
		)
		poller.Dir = dir
		poller.Env = append(os.Environ(), pathEnv)
		if output, pollErr := poller.CombinedOutput(); pollErr != nil {
			t.Fatalf("scheduler worker: %v\n%s", pollErr, output)
		}
		state, readErr := os.ReadFile(statePath)
		return readErr == nil && strings.Contains(string(state), `"phase": "waiting"`)
	}, func() string {
		queue, openErr := scheduler.Open(schedulerDB, scheduler.Options{})
		if openErr != nil {
			return serverOutput.String() + "\nqueue: " + openErr.Error()
		}
		defer queue.Close()
		records, listErr := queue.ListRun(runID)
		return fmt.Sprintf("%s\nqueue=%+v err=%v", serverOutput.String(), records, listErr)
	})
	checkFile(t, filepath.Join(artifactRoot, "manifests", runID+".json"))
	checkFile(t, filepath.Join(dir, ".ai-team", "state", "candidates", runID+".json"))
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool, diagnostics func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout ожидания:\n%s", diagnostics())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestE2E_OpenCodeSandboxEnvironmentReachesRealSubprocess proves the
// isolation environment pkg/runtime.OpenCodeIsolationEnvironment builds
// (deny-by-default permission policy, isolated XDG_CONFIG_HOME, allow-listed
// process environment) actually reaches the real child process spawned via
// exec.Command — not just that the env slice is constructed correctly in
// isolation (already covered by pkg/runtime unit tests), but that a real
// `ai-team run` invocation of the real built binary genuinely delivers it.
// Independent audit Finding 7: this mechanism previously had zero test
// coverage proving a subprocess actually receives it.
func TestE2E_OpenCodeSandboxEnvironmentReachesRealSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}

	captureDir := filepath.Join(t.TempDir(), "env-capture")
	envs := []string{
		pathEnv,
		"MOCK_CAPTURE_ENV_DIR=" + captureDir,
		// MOCK_CAPTURE_ENV_DIR itself needs to be on the allow-list to reach
		// the mock at all — proves the explicit-opt-in half of the
		// allow-list contract, while AI_TEAM_TEST_SECRET_TOKEN below (NOT
		// listed here) proves the deny-by-default half, both at the real
		// subprocess level rather than only in pkg/runtime's unit tests.
		"AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_CAPTURE_ENV_DIR",
		// A secret-shaped variable that must NOT reach the subprocess.
		"AI_TEAM_TEST_SECRET_TOKEN=super-secret-should-not-leak",
	}
	// Runs far enough for analyst (and further stages) to actually execute;
	// final exit code doesn't matter here, only that the mock captured a
	// real environment along the way.
	runAI(t, bin, dir, envs, "run", "--feature", "sandbox-contract", "--task", "sandbox env contract test", "--approve-gates")

	analystEnvPath := filepath.Join(captureDir, "analyst.env")
	data, err := os.ReadFile(analystEnvPath)
	if err != nil {
		t.Fatalf("mock did not capture analyst's environment: %v", err)
	}
	captured := string(data)

	permMatch := regexp.MustCompile(`(?m)^OPENCODE_PERMISSION=(.+)$`).FindStringSubmatch(captured)
	if len(permMatch) != 2 {
		t.Fatalf("OPENCODE_PERMISSION not present in real subprocess environment:\n%s", captured)
	}
	var permission map[string]any
	if err := json.Unmarshal([]byte(permMatch[1]), &permission); err != nil {
		t.Fatalf("OPENCODE_PERMISSION is not valid JSON in real subprocess environment: %v\n%s", err, permMatch[1])
	}
	for _, denied := range []string{"bash", "task", "webfetch", "websearch", "external_directory"} {
		if permission[denied] != "deny" {
			t.Errorf("real subprocess permission policy: %s must be deny, got %#v", denied, permission[denied])
		}
	}

	if !strings.Contains(captured, "OPENCODE_DISABLE_DEFAULT_PLUGINS=true") {
		t.Error("real subprocess must have OPENCODE_DISABLE_DEFAULT_PLUGINS=true")
	}
	if !regexp.MustCompile(`(?m)^XDG_CONFIG_HOME=`).MatchString(captured) {
		t.Error("real subprocess must have an isolated XDG_CONFIG_HOME")
	}
	if !regexp.MustCompile(`(?m)^PATH=`).MatchString(captured) {
		t.Error("real subprocess must still receive PATH (baseline allow-list entry)")
	}
	if strings.Contains(captured, "AI_TEAM_TEST_SECRET_TOKEN") {
		t.Error("a secret-shaped variable not on the allow-list must not reach the real subprocess environment")
	}
}
