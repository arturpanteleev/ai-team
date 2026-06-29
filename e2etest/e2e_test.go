package e2etest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func runAI(t *testing.T, binPath, dir string, envs []string, args ...string) error {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for _, e := range envs {
		cmd.Env = append(cmd.Env, e)
	}
	return cmd.Run()
}

func setupMock(t *testing.T, dir string) string {
	t.Helper()
	projectRoot := findModuleRoot()
	mockSrc := filepath.Join(projectRoot, "e2etest", "mock-opencode.sh")
	mockDir := filepath.Join(dir, "mockbin")
	if err := os.MkdirAll(mockDir, 0755); err != nil {
		t.Fatalf("mkdir mockbin: %v", err)
	}
	mockDst := filepath.Join(mockDir, "opencode")
	if err := os.Symlink(mockSrc, mockDst); err != nil {
		t.Fatalf("symlink opencode mock: %v", err)
	}
	return fmt.Sprintf("PATH=%s%c%s", mockDir, os.PathListSeparator, os.Getenv("PATH"))
}

func artifactsDir(dir, feature string, parts ...string) string {
	return filepath.Join(append([]string{dir, ".ai-team", "artifacts", feature}, parts...)...)
}

func TestE2E_SuccessfulPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t, dir)

	if err := runAI(t, bin, dir, []string{pathEnv}, "init"); err != nil {
		t.Fatalf("ai-team init failed: %v", err)
	}

	checkDir(t, dir, ".ai-team")
	checkDir(t, dir, ".ai-team", "artifacts")

	if err := runAI(t, bin, dir, []string{pathEnv}, "run", "--feature", "e2e-test", "--task", "E2E test task"); err != nil {
		t.Fatalf("ai-team run failed: %v", err)
	}

	feature := "e2e-test"
	checkFile(t, artifactsDir(dir, feature, "proposal.md"))
	checkFile(t, artifactsDir(dir, feature, "specs", "product", "spec.md"))
	checkFile(t, artifactsDir(dir, feature, "design.md"))
	checkFile(t, artifactsDir(dir, feature, "tasks.md"))
	checkFile(t, artifactsDir(dir, feature, "review.md"))
	checkFile(t, artifactsDir(dir, feature, "test-report.md"))
}

func TestE2E_RejectedReviewStopsPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t, dir)

	if err := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=rejected"}, "init"); err != nil {
		t.Fatalf("ai-team init failed: %v", err)
	}

	err := runAI(t, bin, dir, []string{pathEnv, "MOCK_MODE=rejected"}, "run", "--feature", "e2e-rejected", "--task", "Test rejected")
	if err == nil {
		t.Fatal("expected pipeline to fail on rejected review")
	}
	t.Logf("Pipeline correctly failed: %v", err)
}

func TestE2E_InitCreatesStructure(t *testing.T) {
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t, dir)

	if err := runAI(t, bin, dir, []string{pathEnv}, "init"); err != nil {
		t.Fatalf("ai-team init failed: %v", err)
	}

	cfgPath := filepath.Join(dir, ".ai-team", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("config.yaml should exist after init")
	}

	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "opencode") {
		t.Error("config should reference opencode CLI")
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

func checkFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected %s to exist", path)
	}
}
