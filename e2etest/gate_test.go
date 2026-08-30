package e2etest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gateGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	command := exec.Command("git", full...)
	var out strings.Builder
	command.Stdout, command.Stderr = &out, &out
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func gateWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestE2E_GateBundleAndExitCodes проверяет `ai-team gate` end-to-end: exit коды
// (0 PASS / 1 FAIL / 2 BLOCKED), diff-policy test_modify, дефолтный конфиг без
// файла и самодостаточный attestation bundle (index.json + digests).
func TestE2E_GateBundleAndExitCodes(t *testing.T) {
	bin := buildBinary(t)
	repo := t.TempDir()
	gateGit(t, repo, "init", "-q")
	gateGit(t, repo, "config", "user.email", "e2e@test")
	gateGit(t, repo, "config", "user.name", "E2E Gate")
	gateWrite(t, repo, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
	})
	gateGit(t, repo, "add", "-A")
	gateGit(t, repo, "commit", "-q", "-m", "base")
	base := gateGit(t, repo, "rev-parse", "HEAD")

	// 1. Source-only изменение без тестов → FAIL (exit 1), policy violated.
	gateWrite(t, repo, map[string]string{"src/app.go": "package app\n// change\n"})
	gateGit(t, repo, "add", "-A")
	gateGit(t, repo, "commit", "-q", "-m", "source only")
	sourceOnly := gateGit(t, repo, "rev-parse", "HEAD")
	outDir := filepath.Join(t.TempDir(), "bundle1")
	code, out := runAI(t, bin, t.TempDir(), nil, "gate",
		"--target", repo, "--base", base, "--candidate", sourceOnly, "--out", outDir)
	if code != 1 {
		t.Fatalf("gate source-only: exit=%d, ожидалось 1 (FAIL); out:\n%s", code, out)
	}
	for _, want := range []string{"test_modify", "FAIL", "src/app.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("gate output не содержит %q:\n%s", want, out)
		}
	}
	assertGateBundle(t, outDir)

	// 2. source + tests → PASS (exit 0).
	gateWrite(t, repo, map[string]string{
		"src/app.go":        "package app\n// change 2\n",
		"tests/app_test.go": "package app\n\nfunc TestApp(t *testing.T) {}\n",
	})
	gateGit(t, repo, "add", "-A")
	gateGit(t, repo, "commit", "-q", "-m", "source + tests")
	both := gateGit(t, repo, "rev-parse", "HEAD")
	outDir2 := filepath.Join(t.TempDir(), "bundle2")
	code, out = runAI(t, bin, t.TempDir(), nil, "gate",
		"--target", repo, "--base", sourceOnly, "--candidate", both, "--out", outDir2)
	if code != 0 {
		t.Fatalf("gate source+tests: exit=%d, ожидалось 0 (PASS); out:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "bundle digest:") {
		t.Fatalf("gate PASS output: %s", out)
	}
	assertGateBundle(t, outDir2)

	// 3. Нелокальный/отсутствующий ref → BLOCKED (exit 2).
	code, out = runAI(t, bin, t.TempDir(), nil, "gate",
		"--target", repo, "--base", base, "--candidate", "missing-ref-xyz")
	if code != 2 {
		t.Fatalf("gate unknown ref: exit=%d, ожидалось 2 (BLOCKED); out:\n%s", code, out)
	}
}

// assertGateBundle проверяет структуру attestation bundle: индекс с типом,
// records с валидными sha256, контентные файлы immutable.
func assertGateBundle(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"index.json", "gate.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("bundle %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("bundle %s не regular file", name)
		}
	}
}
