package e2etest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}
	runCommand(t, dir, "git", "add", ".gitignore")
	runCommand(t, dir, "git", "commit", "-m", "initialize ai-team ignore policy")
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
	code, out := runAI(t, bin, dir, []string{pathEnv}, "run", "--feature", "e2e-test", "--retry-from", "deployer", "--approve-gates", "--approve-plan", hashMatch[1])
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
	if err != nil || len(runs) != 2 {
		t.Fatalf("ожидались два immutable run для prepare/approve flow: entries=%v err=%v", runs, err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, ".ai-team", "runs", "*", "logs", "*-analyst.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("immutable analyst log отсутствует: logs=%v err=%v", logs, err)
	}

	if !strings.Contains(out, "Пайплайн выполнен") {
		t.Errorf("ожидалось сообщение об успехе:\n%s", out)
	}
	if branch := strings.TrimSpace(runCommand(t, dir, "git", "branch", "--show-current")); branch != "ai-team/e2e-test" {
		t.Errorf("delivery должен создать feature branch, got %q", branch)
	}
	if committed := strings.TrimSpace(runCommand(t, dir, "git", "show", "--name-only", "--format=", "HEAD")); committed != "e2e_implementation.go\ne2e_implementation_test.go" {
		t.Errorf("delivery должен коммитить exact run-attributed file, got %q", committed)
	}
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
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=rejected"},
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
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=fail"},
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
	code, out := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=blocked"},
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

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}

	cfgPath := filepath.Join(dir, ".ai-team", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.yaml should exist after init: %v", err)
	}
	cfg := string(data)
	// Init сериализует полный дефолтный конфиг: гейты и retries не теряются
	for _, want := range []string{"schema_version: 3", "cli: opencode", "checkpoint_after: require_explicit", "max_retries: 2", "stage_timeout: 30m"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.yaml должен содержать %q:\n%s", want, cfg)
		}
	}

	checkDir(t, dir, ".ai-team", "artifacts", "tasks")
	checkDir(t, dir, ".ai-team", "reports")
	checkDir(t, dir, ".ai-team", "logs")
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
