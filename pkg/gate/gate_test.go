package gate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
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

	// untrusted + receipt с PARTIAL осями (trusted-local) → блокируется:
	// AUD-02 требует ENFORCED по всем осям, а не только отсутствия UNAVAILABLE.
	partial := containment.DefaultTrustedLocalReceipt()
	if _, code, err = Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD",
		Config:         &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}},
		AllowUntrusted: true, Receipt: &partial}); code != ExitBlocked || err == nil {
		t.Fatalf("untrusted с PARTIAL receipt: code=%d err=%v", code, err)
	}

	// untrusted + полностью ENFORCED receipt → разрешено (AUD-02).
	enforced := containment.Receipt{
		Profile: "strict",
		Axes: map[containment.Axis]containment.Level{
			containment.AxisFS:   containment.LevelENFORCED,
			containment.AxisNet:  containment.LevelENFORCED,
			containment.AxisProc: containment.LevelENFORCED,
			containment.AxisEnv:  containment.LevelENFORCED,
		},
	}
	if _, code, err = Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD",
		Config:         &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}},
		AllowUntrusted: true, Receipt: &enforced}); code == ExitBlocked || err != nil {
		t.Fatalf("untrusted с ENFORCED receipt: code=%d err=%v", code, err)
	}

	// untrusted + receipt с UNAVAILABLE осью → блокируется.
	unavail := containment.UnavailableReceipt()
	if _, code, err = Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD",
		Config:         &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}},
		AllowUntrusted: true, Receipt: &unavail}); code != ExitBlocked || err == nil {
		t.Fatalf("untrusted с UNAVAILABLE receipt: code=%d err=%v", code, err)
	}

	// untrusted + zero-value receipt (nil Axes, пустой Profile) → блокируется
	// (P1-4 R2 fail-closed): пустой receipt не должен пропускать untrusted
	// через vacuous HasUnavailable.
	empty := containment.Receipt{}
	if _, code, err = Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD",
		Config:         &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}},
		AllowUntrusted: true, Receipt: &empty}); code != ExitBlocked || err == nil {
		t.Fatalf("untrusted с zero-value receipt: code=%d err=%v", code, err)
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

func TestSignalsRecorded(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
		".env.example":      "KEY=placeholder\n",
	})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	feature := commitChange(t, repo, "source + test + secret", map[string]string{
		"src/app.go":             "package app\n// change\n",
		"tests/app_test.go":      "package app\n\nfunc TestApp() {}\n",
		"config/.env.production": "DB=prod-secret\n",
		"deploy/server.key":      "key-material\n",
	})
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: feature, Config: &Config{
		SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitPass {
		t.Fatalf("code=%d, ожидался pass", code)
	}
	sig := result.Signals
	if sig.AddedFiles != 2 || sig.ModifiedFiles != 2 || sig.RemovedFiles != 0 {
		t.Fatalf("signals files: %+v", sig)
	}
	if sig.TestChanges != 1 {
		t.Fatalf("test_changes = %d, ожидался 1 (tests/app_test.go mofified)", sig.TestChanges)
	}
	if sig.AddedLines == 0 || sig.RemovedLines != 0 {
		t.Fatalf("signals lines: %+v", sig)
	}
	if len(sig.SensitivePaths) != 2 {
		t.Fatalf("sensitive paths = %+v, ожидалось 2 (env + key)", sig.SensitivePaths)
	}
	if sig.SensitivePaths[0].Path != "config/.env.production" || sig.SensitivePaths[0].Kind != "env" {
		t.Fatalf("sensitive order/kind: %+v", sig.SensitivePaths)
	}
	if sig.SensitivePaths[1].Path != "deploy/server.key" || sig.SensitivePaths[1].Kind != "secrets" {
		t.Fatalf("sensitive order/kind: %+v", sig.SensitivePaths)
	}
	if sig.ChecksRun != 0 || sig.FailedChecks != 0 {
		t.Fatalf("checks signals: %+v", sig)
	}
}

func TestSignalsRecordFailedChecks(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n", "tests/app_test.go": "package app\n"})
	cfg := &Config{
		SchemaVersion: SchemaVersion,
		DiffPolicy:    DiffPolicy{TestModify: TestModifyOff},
		Checks: []checks.Definition{{
			Name: "must-fail", Class: "unit", Adapter: checks.AdapterCommand, Policy: checks.PolicyRequired,
			Command: []string{"sh", "-c", "exit 1"},
		}},
	}
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: "HEAD", Candidate: "HEAD", Config: cfg})
	if err != nil || code != ExitFail {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if result.Signals.ChecksRun != 1 || result.Signals.FailedChecks != 1 {
		t.Fatalf("checks signals: %+v", result.Signals)
	}
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
		"index identity diverges from gate.json": func(bundleDir string) {
			path := filepath.Join(bundleDir, "index.json")
			indexData, _ := os.ReadFile(path)
			os.WriteFile(path, bytes.Replace(indexData, []byte("aabb"), []byte("0000"), 1), 0644)
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

func TestGateBundleSignAndVerify(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	result := &Result{
		SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy: TestModifyRequired, PolicyVerdict: VerdictPassed,
		Status: "passed", FinishedAt: fixed,
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Без подписи, без ключа — integrity-only pass.
	unsigned := t.TempDir()
	if err := WriteBundle(unsigned, result); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(unsigned); err != nil {
		t.Fatalf("unsigned no-key pass: %v", err)
	}
	// Без подписи, но ключ задан — fail-closed.
	if _, err := VerifyBundle(unsigned, pub); err == nil {
		t.Fatal("unsigned + key должен FAIL (fail-closed)")
	}

	// С подписью: правильный ключ pass, чужой FAIL.
	signed := t.TempDir()
	if err := WriteBundle(signed, result); err != nil {
		t.Fatal(err)
	}
	if err := SignBundle(signed, priv); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(signed, dsse.EnvelopeFileName)); err != nil {
		t.Fatalf("dsse.json отсутствует: %v", err)
	}
	// dsse.json не должен ломать extra-file detection.
	if _, err := VerifyBundle(signed); err != nil {
		t.Fatalf("signed no-key pass: %v", err)
	}
	if _, err := VerifyBundle(signed, pub); err != nil {
		t.Fatalf("signed correct key: %v", err)
	}
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifyBundle(signed, wrongPub); err == nil {
		t.Fatal("signed wrong key должен FAIL")
	}
}

// AUD-01: commit-кандидат должен совпадать с рабочим деревом, иначе checks
// выполнялись бы над другим checkout'ом, а вердикт приписывался бы candidate.
func TestRunBlocksWhenCheckoutDoesNotMatchCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n// v1\n"})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	feature := commitChange(t, repo, "feature", map[string]string{"src/app.go": "package app\n// v2\n"})
	// Чистый checkout base: рабочее дерево != candidate feature.
	gitCmd(t, repo, "checkout", "-q", "-b", "clean", base)

	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: feature, Config: &Config{
		SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired},
	}})
	if code != ExitBlocked || err == nil {
		t.Fatalf("ожидался blocked (checkout != candidate), code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "не совпадает с candidate") {
		t.Fatalf("сообщение не объясняет mismatch: %v", err)
	}
	if result != nil && len(result.Checks) != 0 {
		t.Fatalf("checks не должны выполняться при mismatch checkout: %+v", result.Checks)
	}
}

func TestRunBlocksDirtyCheckoutForCommitCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	head := gitCmd(t, repo, "rev-parse", "HEAD")
	writeFiles(t, repo, map[string]string{"src/app.go": "package app\n// dirty\n"})
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: head, Candidate: "HEAD", Config: &Config{
		SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired},
	}})
	if code != ExitBlocked || err == nil {
		t.Fatalf("ожидался blocked (dirty checkout), code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "не совпадает с candidate") {
		t.Fatalf("сообщение не объясняет mismatch: %v", err)
	}
	if result != nil && len(result.Checks) != 0 {
		t.Fatalf("checks не должны выполняться при dirty checkout: %+v", result.Checks)
	}
}

// AUD-01 / F-4: commit-кандидат требует ровно candidate-дерево. Untracked
// non-ignored файл не входит в candidate, но виден checks в живом рабочем
// дереве и мог бы влиять на PASS, поэтому такой checkout блокируется.
func TestRunBlocksUntrackedContentForCommitCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	head := gitCmd(t, repo, "rev-parse", "HEAD")
	writeFiles(t, repo, map[string]string{"extra/secrets.txt": "secret-data\n"})
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: head, Candidate: head, Config: &Config{
		SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired},
	}})
	if code != ExitBlocked || err == nil {
		t.Fatalf("ожидался blocked (untracked non-ignored содержимое), code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "untracked") {
		t.Fatalf("сообщение должно называть untracked-файлы: %v", err)
	}
	if result != nil && len(result.Checks) != 0 {
		t.Fatalf("checks не должны выполняться при untracked содержимом: %+v", result.Checks)
	}
}

// AUD-01 / F-4: .ai-team (gitignored untracked service-каталог) НЕ должен
// блокировать commit-кандидат — служебное содержимое разрешено.
func TestRunAllowGitignoredServiceDirForCommitCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", ".gitignore")
	gitCmd(t, repo, "commit", "-q", "-m", "add .gitignore")
	head := gitCmd(t, repo, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(repo, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ai-team", "meta.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: head, Candidate: head, Config: &Config{
		SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired},
	}})
	if err != nil || code != ExitPass {
		t.Fatalf(".ai-team (gitignored) не должен блокировать commit-кандидат: code=%d err=%v", code, err)
	}
	if result == nil || result.Status != "passed" {
		t.Fatalf("ожидался PASS, получено: %+v", result)
	}
}

// AUD-01: при совпадающем checkout собственно candidate проходит checks,
// результат привязан к workspace digest, и bundle verify согласован.
func TestRunChecksExactlyAgainstCommitCandidate(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n// v1\n"})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	feature := commitChange(t, repo, "feature", map[string]string{"src/app.go": "package app\n// v2\n"})

	cfg := &Config{
		SchemaVersion: SchemaVersion,
		DiffPolicy:    DiffPolicy{TestModify: TestModifyOff},
		Checks: []checks.Definition{{
			Name: "must-fail", Class: "unit", Adapter: checks.AdapterCommand, Policy: checks.PolicyRequired,
			Command: []string{"sh", "-c", "exit 1"},
		}},
	}
	result, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: feature, Config: cfg})
	if err != nil || code != ExitFail {
		t.Fatalf("clean checkout у feature-кандидата: code=%d err=%v", code, err)
	}
	wantDigest, digestErr := checks.WorkspaceDigest(repo)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	if result.WorkspaceDigest == "" || result.WorkspaceDigest != wantDigest {
		t.Fatalf("workspace digest %q != просканированный checkout %q", result.WorkspaceDigest, wantDigest)
	}
	if len(result.Checks) != 1 || result.Checks[0].WorkspaceDigestAfter != result.WorkspaceDigest {
		t.Fatalf("check workspace binding: digest=%q checks=%+v", result.WorkspaceDigest, result.Checks)
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
		t.Fatalf("digest %s != bundle_sha256 %s", digest, result.BundleSHA256)
	}
}

// AUD-01: VerifyBundle обязан отклонять проверки, привязанные к другому
// workspace identity, чем заявлен в gate.json.
func TestVerifyBundleRejectsCheckWorkspaceMismatch(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	result := &Result{
		SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy: TestModifyRequired, PolicyVerdict: VerdictPassed,
		Status: "passed", FinishedAt: fixed, WorkspaceDigest: "digest-A",
		Checks: []checks.Result{{
			Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
			ExitCode: 0, Status: checks.StatusPassed,
			StartedAt: fixed, FinishedAt: fixed,
			WorkspaceDigestAfter: "digest-B",
		}},
	}
	dir := t.TempDir()
	if err := WriteBundle(dir, result); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(dir); err == nil || !strings.Contains(err.Error(), "workspace identity") {
		t.Fatalf("mismatch workspace identity: должен FAIL, got %v", err)
	}
}

func TestVerifyBundleAcceptsCheckWorkspaceBinding(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	result := &Result{
		SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy: TestModifyRequired, PolicyVerdict: VerdictPassed,
		Status: "passed", FinishedAt: fixed, WorkspaceDigest: "digest-X",
		Checks: []checks.Result{{
			Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
			ExitCode: 0, Status: checks.StatusPassed,
			StartedAt: fixed, FinishedAt: fixed,
			WorkspaceDigestAfter: "digest-X",
		}},
	}
	dir := t.TempDir()
	if err := WriteBundle(dir, result); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(dir); err != nil {
		t.Fatalf("привязанные checks: verify должен пройти, got %v", err)
	}
}

// AUD-04: WriteBundle пишет immutable-артефакты no-follow — повторная запись,
// листовой symlink и symlink-каталог отклоняются без изменения цели.
func TestWriteBundleRejectsExistingAndSymlinks(t *testing.T) {
	base := func() *Result {
		fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		return &Result{
			SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
			BaseCommit: "aabb", CandidateCommit: "ccdd",
			BaseTree: "tree-a", CandidateTree: "tree-b",
			DiffPolicy: TestModifyRequired, PolicyVerdict: VerdictPassed,
			Status: "passed", FinishedAt: fixed,
			Checks: []checks.Result{{
				Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
				Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
				ExitCode: 0, Status: checks.StatusPassed, StartedAt: fixed, FinishedAt: fixed,
			}},
		}
	}

	// Повторная запись bundle не перезаписывает существующие файлы.
	first := t.TempDir()
	if err := WriteBundle(first, base()); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(first, "gate.json"))
	if err := WriteBundle(first, base()); err == nil {
		t.Fatal("повторная WriteBundle должна FAIL (immutable)")
	}
	after, _ := os.ReadFile(filepath.Join(first, "gate.json"))
	if !bytes.Equal(original, after) {
		t.Fatal("gate.json перезаписан второй записью")
	}

	// Листовой symlink gate.json: write должен FAIL, sentinel не меняется.
	leaf := t.TempDir()
	sentinel := filepath.Join(leaf, "sentinel.json")
	os.WriteFile(sentinel, []byte("keep"), 0644)
	os.Symlink(sentinel, filepath.Join(leaf, "gate.json"))
	if err := WriteBundle(leaf, base()); err == nil {
		t.Fatal("gate.json-symlink: WriteBundle должен FAIL")
	}
	if data, _ := os.ReadFile(sentinel); !bytes.Equal(data, []byte("keep")) {
		t.Fatal("sentinel изменён через leaf symlink")
	}

	// Symlink-каталог checks: write должен FAIL без записи вне bundle.
	parent := t.TempDir()
	os.Mkdir(filepath.Join(parent, "checks"), 0755)
	victim := t.TempDir()
	os.Remove(filepath.Join(parent, "checks"))
	os.Symlink(victim, filepath.Join(parent, "checks"))
	if err := WriteBundle(parent, base()); err == nil {
		t.Fatal("checks-symlink: WriteBundle должен FAIL")
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Fatalf("запись ушла через symlink в %s: %v", victim, entries)
	}
}
