package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/gate"
	"github.com/arturpanteleev/ai-team/pkg/redact"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gateRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "config", "user.email", "test@test")
	gitCmd(t, dir, "config", "user.name", "test")
	for name, content := range map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n\nfunc TestApp() {}\n",
	} {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-qm", "init")
	return dir
}

// TestPublishGateBundleBlocksOnSecrets — AUD-11: публикация gate bundle
// обязана пройти privacy scan (fail-closed, D0) ДО записи и подписи. Проверка,
// печатающая github token в output, блокирует публикацию; bundle не создаётся.
func TestPublishGateBundleBlocksOnSecrets(t *testing.T) {
	dir := gateRepo(t)
	token := "ghp_" + strings.Repeat("7", 36)
	cfg := &gate.Config{SchemaVersion: gate.SchemaVersion, DiffPolicy: gate.DiffPolicy{TestModify: gate.TestModifyRequired}}
	cfg.Checks = []checks.Definition{{
		Name: "leak-endpoint", Class: "unit", Adapter: checks.AdapterCommand, Policy: checks.PolicyRequired,
		Command: []string{"sh", "-c", "printf 'GITHUB_TOKEN=" + token + "'"},
	}}
	result, code, err := gate.Run(context.Background(), gate.Options{TargetDir: dir, Base: "HEAD", Candidate: "HEAD", Config: cfg})
	if err != nil {
		t.Fatalf("gate.Run: %v", err)
	}
	if code != gate.ExitPass || result.Status != "passed" {
		t.Fatalf("проверка должна пройти, got code=%d status=%s", code, result.Status)
	}

	policy := redact.Policy{FailOnSecrets: true, RepoRoot: dir}
	out := filepath.Join(dir, ".ai-team", "gates", "aud11")
	if err := publishGateBundle(out, result, policy, nil); err == nil {
		t.Fatal("bundle с secret в check output обязан блокироваться")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("bundle не должен быть опубликован при секретах")
	}

	// Чистый run публикуется; повторная публикация в существующий out
	// отклоняется (bundle immutable).
	cleanCfg := &gate.Config{SchemaVersion: gate.SchemaVersion, DiffPolicy: gate.DiffPolicy{TestModify: gate.TestModifyRequired}}
	result2, code2, err := gate.Run(context.Background(), gate.Options{TargetDir: dir, Base: "HEAD", Candidate: "HEAD", Config: cleanCfg})
	if err != nil || code2 != gate.ExitPass {
		t.Fatalf("повторный gate.Run: code=%d err=%v", code2, err)
	}
	if err := publishGateBundle(out, result2, policy, nil); err != nil {
		t.Fatalf("публикация чистого bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Fatalf("bundle не опубликован: %v", err)
	}
	if err := publishGateBundle(out, result2, policy, nil); err == nil {
		t.Fatal("повторная публикация в существующий каталог должна отклоняться")
	}

	// Подписанный bundle (DSSE) вычисляется по безопасным records: тех же
	// check output, что и при публикации без подписи.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(dir, ".ai-team", "gates", "aud11-signed")
	if err := publishGateBundle(out2, result2, policy, priv); err != nil {
		t.Fatalf("публикация подписанного bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out2, "dsse.json")); err != nil {
		t.Fatalf("подписанный bundle должен содержать dsse.json: %v", err)
	}
}
