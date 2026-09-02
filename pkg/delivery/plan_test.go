package delivery

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

func TestPlanStrictValidationAndStableHash(t *testing.T) {
	plan := validTestPlan()
	plan.Files = []string{"a.go", "b.go"}
	plan.FileDigests["b.go"] = strings.Repeat("e", 64)
	plan.FileModes["b.go"] = "100644"
	first, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	plan.Files = []string{"b.go", "a.go"}
	second, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash должен быть независим от порядка files: %s != %s", first, second)
	}

	for name, data := range map[string]string{
		"unknown":  `{"schema_version":1,"branch":"ai-team/x","base_branch":"main","remote":"origin","files":["a.go"],"commit_message":"x","pr_title":"x","pr_body":"x","extra":true}`,
		"trailing": `{"schema_version":1,"branch":"ai-team/x","base_branch":"main","remote":"origin","files":["a.go"],"commit_message":"x","pr_title":"x","pr_body":"x"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("невалидный plan должен быть отклонён")
			}
		})
	}
	plan.Branch = "main"
	if err := plan.Validate(); err == nil {
		t.Fatal("protected branch должна быть отклонена")
	}
	plan = validTestPlan()
	plan.Files = []string{".ai-team/secret"}
	if err := plan.Validate(); err == nil {
		t.Fatal("control files должны быть отклонены")
	}
}

func TestPreparedStateRejectsPlanTampering(t *testing.T) {
	target := t.TempDir()
	plan := validTestPlan()
	statePath, err := Prepare(target, "feat", plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), "feat изменить a", "feat изменить b", 1)
	if err := os.WriteFile(statePath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPreparedPlan(target, "feat"); err == nil || !strings.Contains(err.Error(), "plan hash") {
		t.Fatalf("tampered canonical plan must be rejected, got %v", err)
	}
}

func TestCompletedStateIsArchivedBeforeNewPlan(t *testing.T) {
	target := t.TempDir()
	firstPlan := validTestPlan()
	firstHash, err := firstPlan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := deliveryStatePath(target, "feat")
	if err != nil {
		t.Fatal(err)
	}
	completed := &state{
		SchemaVersion: deliveryStateSchemaVersion, PlanHash: firstHash, Plan: firstPlan,
		CommitSHA: strings.Repeat("f", 40), CommitVerified: true, Pushed: true, PRURL: "https://example.test/pr/1",
	}
	if err := writeState(statePath, completed); err != nil {
		t.Fatal(err)
	}
	secondPlan := validTestPlan()
	secondPlan.CommitMessage = "feat изменить b"
	secondPlan.PRTitle = "feat изменить b"
	secondHash, err := secondPlan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateState(statePath, secondHash, secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PlanHash != secondHash || loaded.CommitSHA != "" {
		t.Fatalf("new state was not initialized: %+v", loaded)
	}
	archive := strings.TrimSuffix(statePath, filepath.Ext(statePath)) + "." + firstHash + ".completed.json"
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("completed state archive missing: %v", err)
	}
}

func TestConfiguredFilterPathsPreservesGitCheckAttrRecords(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "unspecified z path", output: "z.go\x00filter\x00unspecified\x00"},
		{name: "unset", output: "a.go\x00filter\x00unset\x00"},
		{name: "configured", output: "z.go\x00filter\x00lfs\x00", want: "z.go"},
		{name: "multi record", output: "z.go\x00filter\x00unspecified\x00a.go\x00filter\x00custom\x00", want: "a.go"},
		{name: "malformed", output: "z.go\x00filter\x00", want: "<malformed-check-attr-output>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := strings.Join(configuredFilterPaths(test.output), ","); got != test.want {
				t.Fatalf("configuredFilterPaths=%q want %q", got, test.want)
			}
		})
	}
}

func TestBuildPlanUsesOnlyAttributedFiles(t *testing.T) {
	repo, _ := setupRepository(t)
	writeFile(t, filepath.Join(repo, "z.go"), "package z\n")
	plan, err := BuildPlan(context.Background(), repo, "feat", "Добавить полезную функцию", []string{"z.go", ".ai-team/state", "a.go", "a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Files, ",") != "z.go" || plan.Branch != "ai-team/feat" || plan.BaseBranch != "main" {
		t.Fatalf("неверный детерминированный plan: %+v", plan)
	}
}

func TestControllerAddsProvenanceTrailersToCommit(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// changed\n")
	plan, err := BuildPlan(context.Background(), repo, "feat", "change", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	trailers := []string{
		TrailerRunID + ": run-abcdef123456",
		TrailerRuntime + ": 0f2b5a11c9d34e55809f",
		TrailerAttestation + ": 5f0d6b2c3a49e781f6a2",
	}
	result, err := NewController().Execute(context.Background(), Request{
		TargetDir: repo, Feature: "feat", Plan: plan, Trailers: trailers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitSHA == "" {
		t.Fatalf("commit не создан: %+v", result)
	}
	message := git(t, repo, "show", "-s", "--format=%B", "HEAD")
	expected := FullCommitMessage(plan.CommitMessage, trailers)
	if strings.TrimRight(message, "\r\n") != expected {
		t.Fatalf("commit message не содержит trailers:\n--- got ---\n%s\n--- want ---\n%s", message, expected)
	}
	show := git(t, repo, "show", "-s", "--format=%B", "HEAD")
	if !strings.Contains(show, TrailerRunID+": run-abcdef123456") {
		t.Fatalf("trailer ai-team-run отсутствует:\n%s", show)
	}

	// Resume/idempotent повтор: те же trailers допускаются, сообщение сверяется
	// с сохранённым состоянием.
	resumed, err := NewController().Execute(context.Background(), Request{
		TargetDir: repo, Feature: "feat", Plan: plan, Trailers: trailers,
	})
	if err != nil || resumed.CommitSHA != result.CommitSHA {
		t.Fatalf("resume с теми же trailers нескомпенсирован: err=%v result=%+v", err, resumed)
	}

	// «Краш после git commit до записи identity»: очищаем commit_sha в state —
	// Execute должен восстановить точный approved commit и сверить trailers.
	var raw struct {
		CommitSHA string `json:"commit_sha"`
	}
	data, err := os.ReadFile(result.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.CommitSHA != result.CommitSHA {
		t.Fatalf("state.commit_sha=%q, ожидался %s", raw.CommitSHA, result.CommitSHA)
	}
	synthetic := strings.Replace(string(data), `"commit_sha":"`+result.CommitSHA+`"`, `"commit_sha":""`, 1)
	if err := os.WriteFile(result.StatePath, []byte(synthetic), 0644); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewController().Execute(context.Background(), Request{
		TargetDir: repo, Feature: "feat", Plan: plan,
	})
	if err != nil {
		t.Fatalf("crash recovery с trailers: %v", err)
	}
	if recovered.CommitSHA != result.CommitSHA {
		t.Fatalf("recovery вернул другой commit: %s != %s", recovered.CommitSHA, result.CommitSHA)
	}
}

func TestValidateTrailersRejectsBadForm(t *testing.T) {
	cases := [][]string{
		{"ai-team-run: "},
		{": value"},
		{"ai-team-run: run-123", "ai-team-run: run-456"},
		{"unknown-key: value"},
		{"ai-team-attestation: 123 456"},
		{"ai-team-run:run-123"},
	}
	for _, trailers := range cases {
		if err := ValidateTrailers(trailers); err == nil {
			t.Fatalf("trailers %v должны быть отклонены", trailers)
		}
	}
	good := []string{"ai-team-run: run-abc", "ai-team-runtime: ab12", "ai-team-attestation: 5f0d_1-2a"}
	if err := ValidateTrailers(good); err != nil {
		t.Fatalf("валидные trailers отклонены: %v", err)
	}
}

func TestTerminalRecordRoundtripAndTamper(t *testing.T) {
	runDir := t.TempDir()
	record := TerminalRecord{
		SchemaVersion:     TerminalRecordSchemaVersion,
		RunID:             "20260901T120000.000000000Z-abcdef0123456789",
		Feature:           "feat",
		PlanHash:          strings.Repeat("c", 64),
		CommitSHA:         strings.Repeat("a", 40),
		PRURL:             "https://example.test/pr/1",
		Trailers:          []string{"ai-team-run: 20260901T120000.000000000Z-abcdef0123456789", "ai-team-runtime: " + strings.Repeat("b", 64)},
		AttestationSHA256: "",
		RuntimeIdentity:   strings.Repeat("b", 64),
		PerformedAt:       time.Now().UTC(),
	}
	// Attestation trailer обязан совпадать с полем — должен быть отклонён.
	record.Trailers = []string{
		"ai-team-run: " + record.RunID,
		"ai-team-runtime: " + record.RuntimeIdentity,
		"ai-team-attestation: " + strings.Repeat("e", 64),
	}
	record.AttestationSHA256 = strings.Repeat("d", 64)
	if err := WriteTerminalRecord(runDir, record); err == nil {
		t.Fatal("record с несогласованным attestation trailer должен быть отклонён")
	}
	record.Trailers = []string{
		"ai-team-run: " + record.RunID,
		"ai-team-runtime: " + record.RuntimeIdentity,
		"ai-team-attestation: " + record.AttestationSHA256,
	}
	if err := WriteTerminalRecord(runDir, record); err != nil {
		t.Fatalf("валидный record должен быть записан: %v", err)
	}
	got, ok, err := ReadTerminalRecord(runDir)
	if err != nil || !ok || got.PlanHash != record.PlanHash || len(got.Trailers) != 3 {
		t.Fatalf("roundtrip failed: %+v ok=%v err=%v", got, ok, err)
	}
	// Идемпотентный retry допускается, произвольная перезапись — нет.
	if err := WriteTerminalRecord(runDir, record); err != nil {
		t.Fatalf("идемпотентный repeat должен быть допущен: %v", err)
	}
	other := record
	other.PlanHash = strings.Repeat("d", 64)
	if err := WriteTerminalRecord(runDir, other); err == nil {
		t.Fatal("перезапись существующего record должна быть запрещена")
	}
	// Tamper: правка commit_sha должна быть обнаружена.
	data, err := os.ReadFile(filepath.Join(runDir, "delivery.json"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(string(data), record.CommitSHA, strings.Repeat("f", 40), 1))
	if err := os.WriteFile(filepath.Join(runDir, "delivery.json"), tampered, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTerminalRecord(runDir); err == nil {
		t.Fatal("tampered record должен быть отклонён")
	}
	if _, ok, err := ReadTerminalRecord(t.TempDir()); err != nil || ok {
		t.Fatalf("отсутствующий record: ok=%v err=%v", ok, err)
	}
}

func TestControllerStagesExactFilesAndCreatesPR(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// changed\n")
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "user change\n")
	plan, err := BuildPlan(context.Background(), repo, "feat", "change", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}

	result, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "feat", Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitSHA == "" || result.PRURL != "https://example.test/pr/1" || len(result.Steps) == 0 {
		t.Fatalf("неполный delivery result: %+v", result)
	}
	changed := git(t, repo, "show", "--name-only", "--format=", "HEAD")
	if strings.TrimSpace(changed) != "a.go" {
		t.Fatalf("commit должен содержать только approved file, got %q", changed)
	}
	status := git(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "unrelated.txt") || strings.Contains(status, "a.go") {
		t.Fatalf("unrelated change должна остаться незакоммиченной: %q", status)
	}
	if _, err := os.Stat(result.StatePath); err != nil {
		t.Fatalf("delivery state не сохранён: %v", err)
	}
}

func TestControllerVerifiesLargeCommittedBlob(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	content := strings.Repeat("large delivery evidence\n", 32*1024)
	writeFile(t, filepath.Join(repo, "large.txt"), content)
	plan, err := BuildPlan(context.Background(), repo, "large", "large file", []string{"large.txt"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}

	result, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "large", Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	got := git(t, repo, "cat-file", "-s", "HEAD:large.txt")
	if result.CommitSHA == "" || strings.TrimSpace(got) != "786432" {
		t.Fatalf("large blob was not committed exactly: result=%+v size=%q", result, got)
	}
}

func TestControllerPreservesExecutableMode(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	script := filepath.Join(repo, "run.sh")
	writeFile(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(context.Background(), repo, "executable", "add executable", []string{"run.sh"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if plan.FileModes["run.sh"] != "100755" {
		t.Fatalf("planner mode=%q", plan.FileModes["run.sh"])
	}

	if _, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "executable", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(git(t, repo, "ls-tree", "HEAD", "--", "run.sh"))[0]; got != "100755" {
		t.Fatalf("committed mode=%q", got)
	}
}

func TestControllerRejectsGitEOLNormalizationBeforeCommit(t *testing.T) {
	repo, _ := setupRepository(t)
	writeFile(t, filepath.Join(repo, ".gitattributes"), "*.go text eol=lf\n")
	git(t, repo, "add", ".gitattributes")
	git(t, repo, "commit", "-m", "attributes")
	git(t, repo, "push", "origin", "main")
	baseline := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(repo, "a.go"), "package a\r\n// normalized\r\n")
	plan, err := BuildPlan(context.Background(), repo, "eol", "normalize", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "eol", Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "staged bytes") {
		t.Fatalf("expected staged-byte mismatch, got %v", err)
	}
	if head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD")); head != baseline {
		t.Fatalf("normalization failure created a commit: head=%s baseline=%s", head, baseline)
	}
	if staged := strings.TrimSpace(git(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Fatalf("normalization failure left staged data: %q", staged)
	}
}

func TestControllerResumesAfterPartialDelivery(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	t.Setenv("AI_TEAM_FAKE_GH_FAIL", "1")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// partial\n")
	plan, err := BuildPlan(context.Background(), repo, "partial", "partial", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{TargetDir: repo, Feature: "partial", Plan: plan}

	first, err := NewController().Execute(context.Background(), request)
	if err == nil || first.CommitSHA == "" || first.PRURL != "" {
		t.Fatalf("первый запуск должен остановиться после commit/push: result=%+v err=%v", first, err)
	}
	t.Setenv("AI_TEAM_FAKE_GH_FAIL", "0")
	second, err := NewController().Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("resume должен завершить PR без повторного commit: %v", err)
	}
	if second.CommitSHA != first.CommitSHA || second.PRURL == "" {
		t.Fatalf("resume result: first=%+v second=%+v", first, second)
	}
	if count := strings.TrimSpace(git(t, repo, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("resume не должен создавать второй delivery commit: %s", count)
	}
}

func TestControllerRecoversCommitAfterStatePersistenceGap(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// recovered\n")
	plan, err := BuildPlan(context.Background(), repo, "recover", "recover", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{TargetDir: repo, Feature: "recover", Plan: plan}
	first, err := (&Controller{Runner: &failRecordCommitRunner{}}).Execute(context.Background(), request)
	if err == nil || first.CommitSHA != "" {
		t.Fatalf("first run must fail after commit but before identity persistence: result=%+v err=%v", first, err)
	}
	if head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD")); head == plan.BaselineHead {
		t.Fatal("commit effect was not created before injected persistence gap")
	}
	second, err := NewController().Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("exact unrecorded commit must be recovered: %v", err)
	}
	if second.CommitSHA == "" || second.PRURL == "" {
		t.Fatalf("recovered delivery is incomplete: %+v", second)
	}
	if count := strings.TrimSpace(git(t, repo, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("recovery created a duplicate commit: %s", count)
	}
}

func TestControllerRejectsUnrecordedCommitWithDifferentMessage(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// unexpected author\n")
	plan, err := BuildPlan(context.Background(), repo, "reject-recovery", "approved message", []string{"a.go"}, testVerification(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "switch", "-c", plan.Branch)
	git(t, repo, "add", "--", "a.go")
	git(t, repo, "commit", "-m", "different message")
	_, err = NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "reject-recovery", Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "commit message") {
		t.Fatalf("unrecorded foreign commit must fail closed: %v", err)
	}
	statePath, pathErr := deliveryStatePath(repo, "reject-recovery")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	data, readErr := os.ReadFile(statePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	loaded, parseErr := parseState(data)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if loaded.CommitSHA != "" || loaded.CommitVerified {
		t.Fatalf("rejected commit leaked into durable state: %+v", loaded)
	}
}

type failRecordCommitRunner struct {
	delegate ExecRunner
	failed   bool
}

func (runner *failRecordCommitRunner) Run(ctx context.Context, dir, name string, args ...string) StepResult {
	if !runner.failed && name == "git" && len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
		runner.failed = true
		now := time.Now().UTC()
		return StepResult{Command: append([]string{name}, args...), StartedAt: now, FinishedAt: now, ExitCode: 1, Status: StepFailed, Reason: "injected post-commit persistence gap"}
	}
	return runner.delegate.Run(ctx, dir, name, args...)
}

func TestControllerRefusesPreStagedFiles(t *testing.T) {
	repo, _ := setupRepository(t)
	installFakeGH(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n// changed\n")
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "staged user change\n")
	git(t, repo, "add", "unrelated.txt")
	plan, buildErr := BuildPlan(context.Background(), repo, "staged", "staged", []string{"a.go"}, testVerification(t, repo))
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	_, err := NewController().Execute(context.Background(), Request{TargetDir: repo, Feature: "staged", Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "index уже содержит staged files") {
		t.Fatalf("pre-staged data должна быть защищена: %v", err)
	}
}

func validTestPlan() Plan {
	return Plan{
		SchemaVersion: SchemaVersion, Branch: "ai-team/feat", BaseBranch: "main", Remote: "origin",
		Files: []string{"a.go"}, FileDigests: map[string]string{"a.go": strings.Repeat("a", 64)}, FileModes: map[string]string{"a.go": "100644"},
		BaselineHead: strings.Repeat("b", 40), SourceRunID: "run-1",
		VerifiedWorkspaceDigest: strings.Repeat("c", 64), CheckEvidenceDigest: strings.Repeat("d", 64),
		Preconditions: map[string]PreconditionEvidence{
			"review": {Type: "file", Size: 10, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
		},
		CommitMessage: "feat изменить a", PRTitle: "feat изменить a",
		PRBody: "Что изменено: a.go.\nЗачем: тест.\nПроверка: tests passed.",
	}
}

func testVerification(t *testing.T, target string) Verification {
	t.Helper()
	digest, err := checks.WorkspaceDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	return Verification{SourceRunID: "run-1", WorkspaceDigest: digest, CheckEvidenceDigest: strings.Repeat("d", 64), Preconditions: map[string]PreconditionEvidence{
		"review": {Type: "file", Size: 10, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
	}}
}

func setupRepository(t *testing.T) (string, string) {
	t.Helper()
	repo, remote := t.TempDir(), filepath.Join(t.TempDir(), "remote.git")
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "AI Team Test")
	git(t, repo, "config", "user.email", "ai-team@example.test")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "clean\n")
	git(t, repo, "add", "a.go", "unrelated.txt")
	git(t, repo, "commit", "-m", "initial")
	git(t, filepath.Dir(remote), "init", "--bare", remote)
	git(t, repo, "remote", "add", "origin", remote)
	git(t, repo, "push", "-u", "origin", "main")
	return repo, remote
}

func installFakeGH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "created")
	script := `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  if [ ! -f "` + marker + `" ]; then
    exit 1
  fi
  oid=$(git rev-parse HEAD)
  printf '{"url":"https://example.test/pr/1","state":"OPEN","baseRefName":"main","headRefName":"%s","headRefOid":"%s"}\n' "$3" "$oid"
  exit 0
fi
if [ "$AI_TEAM_FAKE_GH_FAIL" = "1" ]; then
  echo "simulated gh failure" >&2
  exit 2
fi
touch "` + marker + `"
echo "https://example.test/pr/1"
`
	path := filepath.Join(dir, "gh")
	writeFile(t, path, script)
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AI_TEAM_FAKE_GH_FAIL", "0")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
