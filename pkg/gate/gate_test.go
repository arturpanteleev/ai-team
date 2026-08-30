package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"gopkg.in/yaml.v3"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	command := exec.Command("git", full...)
	var buffer bytes.Buffer
	command.Stdout, command.Stderr = &buffer, &buffer
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, buffer.String())
	}
	return strings.TrimSpace(buffer.String())
}

func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "config", "user.email", "gate@test")
	gitCmd(t, dir, "config", "user.name", "Gate Test")
	writeFiles(t, dir, files)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
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

func commitChange(t *testing.T, dir string, message string, files map[string]string) string {
	t.Helper()
	writeFiles(t, dir, files)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", message)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func yamlConfig(t *testing.T, data string) *Config {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("config decode: %v", err)
	}
	return &cfg
}

func TestConfigStrictValidation(t *testing.T) {
	good := yamlConfig(t, "schema_version: 1\ndiff_policy:\n  test_modify: required\n")
	if good.DiffPolicy.TestModify != TestModifyRequired {
		t.Fatalf("unexpected policy %q", good.DiffPolicy.TestModify)
	}
	cases := []string{
		"schema_version: 2\ndiff_policy:\n  test_modify: required\n",
		"schema_version: 1\nunknown_key: true\ndiff_policy:\n  test_modify: required\n",
		"schema_version: 1\ndiff_policy:\n  test_modify: sometimes\n",
		"schema_version: 1\ndiff_policy:\n  unknown: off\n",
		"schema_version: 1\ndiff_policy:\n  test_modify: off\nchecks:\n  - name: x\n    class: unit\n    command: [x]\n    unknown: y\n",
	}
	for _, data := range cases {
		var cfg Config
		if err := yaml.Unmarshal([]byte(data), &cfg); err == nil {
			t.Fatalf("ожидалась ошибка для конфига:\n%s", data)
		}
	}
}

func TestRunPolicyRequiredPassesAndFails(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
	})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	sourceOnly := commitChange(t, repo, "source only", map[string]string{"src/app.go": "package app\n// change\n"})
	cfg := &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired}}

	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: sourceOnly, Config: cfg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFail || result.Status != "failed" || result.PolicyVerdict != VerdictViolated {
		t.Fatalf("source-only: code=%d verdict=%s status=%s, ожидалось fail/violated", code, result.PolicyVerdict, result.Status)
	}
	if len(result.PolicyViolations) != 1 || result.PolicyViolations[0].Path != "src/app.go" {
		t.Fatalf("violations = %+v", result.PolicyViolations)
	}
	if len(result.Mutations) != 1 || result.Mutations[0].Class != "source" || result.Mutations[0].Kind != KindModified {
		t.Fatalf("mutations = %+v", result.Mutations)
	}

	both := commitChange(t, repo, "source + tests", map[string]string{
		"src/app.go":        "package app\n// change 2\n",
		"tests/app_test.go": "package app\n\nfunc TestApp() {}\n",
	})
	result, code, err = Run(context.Background(), Options{TargetDir: repo, Base: sourceOnly, Candidate: both, Config: cfg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitPass || result.PolicyVerdict != VerdictPassed || result.Status != "passed" {
		t.Fatalf("source+tests: code=%d verdict=%s status=%s, ожидалось pass", code, result.PolicyVerdict, result.Status)
	}
}

func TestRunPolicyWarningAndOff(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
	})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	sourceOnly := commitChange(t, repo, "source only", map[string]string{"src/app.go": "package app\n// change\n"})

	cfg := &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyWarning}}
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: sourceOnly, Config: cfg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitPass || result.PolicyVerdict != VerdictWarning || result.Status != "passed" {
		t.Fatalf("warning: code=%d verdict=%s status=%s", code, result.PolicyVerdict, result.Status)
	}
	if len(result.PolicyViolations) != 1 {
		t.Fatalf("warning violations = %+v", result.PolicyViolations)
	}

	cfg = &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}}
	result, code, err = Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: sourceOnly, Config: cfg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitPass || result.PolicyVerdict != VerdictSkipped || len(result.PolicyViolations) != 0 {
		t.Fatalf("off: code=%d verdict=%s violations=%+v", code, result.PolicyVerdict, result.PolicyViolations)
	}
}

func TestRunWorktreeCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
	})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	writeFiles(t, repo, map[string]string{"src/app.go": "package app\n// wip\n"})
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: "WORKTREE",
		Config: &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired}}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFail {
		t.Fatalf("worktree candidate: code=%d, ожидалось fail", code)
	}
	if result.CandidateCommit != "" || result.CandidateTree == "" {
		t.Fatalf("WORKTREE candidate: commit=%q tree=%q", result.CandidateCommit, result.CandidateTree)
	}
}

func TestBlockedOnUnknownRefAndUntrusted(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	cfg := &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}}
	_, code, err := Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "missing-ref-xyz", Config: cfg})
	if code != ExitBlocked {
		t.Fatalf("unknown ref: code=%d, ожидалось blocked", code)
	}
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("ожидался BlockedError, got %v", err)
	}
	if !strings.Contains(err.Error(), "trusted local") {
		t.Fatalf("сообщение не объясняет trusted-only: %v", err)
	}

	_, code, err = Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD",
		Config: &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}}, AllowUntrusted: true})
	if code != ExitBlocked || err == nil {
		t.Fatalf("untrusted flag: code=%d err=%v", code, err)
	}
}

func TestBlockedOnNonGitTarget(t *testing.T) {
	dir := t.TempDir()
	_, code, err := Run(context.Background(), Options{TargetDir: dir, Base: "HEAD", Candidate: "HEAD",
		Config: &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}}})
	if code != ExitBlocked || err == nil {
		t.Fatalf("non-git target: code=%d err=%v", code, err)
	}
}

func TestRequiredCheckFailure(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	cfg := &Config{
		SchemaVersion: SchemaVersion,
		DiffPolicy:    DiffPolicy{TestModify: TestModifyOff},
		Checks: []checks.Definition{{
			Name: "must-fail", Class: "unit", Adapter: checks.AdapterCommand, Policy: checks.PolicyRequired,
			Command: []string{"sh", "-c", "exit 1"},
		}},
	}
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD", Config: cfg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFail || result.Status != "failed" {
		t.Fatalf("required check: code=%d status=%s", code, result.Status)
	}
	if len(result.Checks) != 1 || result.Checks[0].Status != checks.StatusFailed {
		t.Fatalf("checks = %+v", result.Checks)
	}
}

func TestBundleDeterminismAndDigests(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := &Result{
		SchemaVersion: SchemaVersion,
		Base:          "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy:    TestModifyRequired,
		Mutations:     []Mutation{{Path: "src/app.go", Kind: KindModified, Class: "source"}},
		PolicyVerdict: VerdictPassed,
		Status:        "passed",
		FinishedAt:    fixed,
		Checks: []checks.Result{{
			Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
			ExitCode: 0, Status: checks.StatusPassed, StartedAt: fixed, FinishedAt: fixed,
		}},
	}
	first, err := writeBundleCopy(t, base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeBundleCopy(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("bundle digest недетерминирован: %s != %s", first, second)
	}
}

func writeBundleCopy(t *testing.T, result *Result) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := WriteBundle(dir, result); err != nil {
		return "", err
	}
	if result.BundleSHA256 == "" {
		t.Fatal("BundleSHA256 не заполнен")
	}
	indexData, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatal(err)
	}
	if index.SchemaVersion != SchemaVersion || index.Type != BundleType {
		t.Fatalf("index = %+v", index)
	}
	if len(index.Records) != 2 || index.Records[0].Path > index.Records[1].Path {
		t.Fatalf("records = %+v (ожидалось 2, sorted)", index.Records)
	}
	for _, record := range index.Records {
		fileData, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(record.Path)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(fileData)
		if hex.EncodeToString(sum[:]) != record.SHA256 {
			t.Fatalf("record %s digest mismatch", record.Path)
		}
	}
	return result.BundleSHA256, nil
}

func TestVerifyBundleRoundtripAndTampering(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	result := &Result{
		SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy:    TestModifyRequired,
		Mutations:     []Mutation{{Path: "src/app.go", Kind: KindModified, Class: "source"}},
		PolicyVerdict: VerdictPassed, Status: "passed", FinishedAt: fixed,
		Checks: []checks.Result{{
			Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
			ExitCode: 0, Status: checks.StatusPassed, StartedAt: fixed, FinishedAt: fixed,
		}},
	}
	dir := t.TempDir()
	if err := WriteBundle(dir, result); err != nil {
		t.Fatal(err)
	}
	digest, err := VerifyBundle(dir)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if digest != result.BundleSHA256 {
		t.Fatalf("verify digest %s != result.BundleSHA256 %s", digest, result.BundleSHA256)
	}

	tamperCases := map[string]func(bundleDir string){
		"tamper check record": func(bundleDir string) {
			path := filepath.Join(bundleDir, "checks", "001-go-test.json")
			check, _ := os.ReadFile(path)
			_ = os.Chmod(path, 0644)
			os.WriteFile(path, append(check, []byte("\ntampered")...), 0644)
		},
		"extra file": func(bundleDir string) {
			os.WriteFile(filepath.Join(bundleDir, "sneaky.json"), []byte("{}"), 0644)
		},
		"foreign type": func(bundleDir string) {
			path := filepath.Join(bundleDir, "index.json")
			indexData, _ := os.ReadFile(path)
			os.WriteFile(path, bytes.Replace(indexData, []byte(BundleType), []byte("other-bundle"), 1), 0644)
		},
		"wrong declared digest": func(bundleDir string) {
			path := filepath.Join(bundleDir, "gate.json")
			gateData, _ := os.ReadFile(path)
			_ = os.Chmod(path, 0644)
			body := strings.TrimSuffix(string(gateData), "\n")
			fake := strings.Repeat("0", 64)
			if body != "" && strings.HasSuffix(body, "}") {
				body = body[:len(body)-1] + `,
  "bundle_sha256": "` + fake + `"
}`
			}
			os.WriteFile(path, []byte(body), 0644)
		},
		"missing record": func(bundleDir string) {
			os.Remove(filepath.Join(bundleDir, "checks", "001-go-test.json"))
		},
	}
	for name, mutate := range tamperCases {
		fresh := t.TempDir()
		if err := WriteBundle(fresh, result); err != nil {
			t.Fatal(err)
		}
		mutate(fresh)
		if _, err := VerifyBundle(fresh); err == nil {
			t.Fatalf("tamper %q: VerifyBundle не обнаружил повреждение", name)
		}
	}
}
