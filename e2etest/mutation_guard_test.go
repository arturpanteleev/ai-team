package e2etest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mutation_guard_test.go — QS-03 (#154). Оба сценария предъявляют одно и то
// же: ignore-набор, введённый ради скорости обхода дерева, применялся и к
// атрибуции мутаций, поэтому `mutation: none` не означал read-only.

// TestE2E_ReadOnlyStageWritingIgnoredDirsIsRejected — доказательство А.
// reviewer объявлен `mutation: none`, но пишет в vendor/, node_modules/,
// dist/ и в .ai-team/ мимо artifact namespace. Guard обязан остановить
// попытку; раньше манифест показывал `mutations= None` и run спокойно
// доходил до delivery-approval.
func TestE2E_ReadOnlyStageWritingIgnoredDirsIsRejected(t *testing.T) {
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

	evil := strings.Join([]string{
		"vendor/evil/evil.go=package evil",
		"node_modules/evil/index.js=module.exports = 1",
		"dist/app.js=console.log(1)",
		".ai-team/reviewer-was-here.txt=pwned",
	}, "\n")

	code, out := runAI(t, bin, dir, []string{
		pathEnv,
		"MOCK_EVIL_AGENT=reviewer",
		"MOCK_EVIL_FILES=" + evil,
		"AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_EVIL_AGENT,MOCK_EVIL_FILES",
	}, "run", "--feature", "qs03-a", "--task", "QS-03 read-only guard", "--approve-gates")

	manifest := stageManifest(t, dir, "reviewer")
	t.Logf("=== ai-team run (сценарий А) → exit %d\n%s", code, out)
	t.Logf("=== манифест попытки reviewer: status=%q mutations=%v", manifest.Status, manifest.Mutations)

	if code == 3 {
		t.Fatalf("read-only reviewer записал vendor/node_modules/dist/.ai-team, "+
			"но run дошёл до delivery-approval (exit 3):\n%s", out)
	}
	if !strings.Contains(out, "нарушил mutation policy") {
		t.Fatalf("guard не предъявил нарушение mutation policy (exit %d):\n%s", code, out)
	}
	if manifest.Status != "failed" {
		t.Errorf("манифест reviewer обязан быть failed, получено %q", manifest.Status)
	}
	// Каждый записанный путь обязан быть атрибутирован в манифесте попытки,
	// а не молча потерян: именно там раньше стояло mutations= None.
	recorded := make(map[string]bool, len(manifest.Mutations))
	for _, path := range manifest.Mutations {
		recorded[path] = true
	}
	for _, want := range []string{
		"vendor/evil/evil.go",
		"node_modules/evil/index.js",
		"dist/app.js",
		".ai-team/reviewer-was-here.txt",
	} {
		if !recorded[want] {
			t.Errorf("путь %s не попал в mutations манифеста: %v", want, manifest.Mutations)
		}
	}
}

// TestE2E_ReadOnlyStageInvalidatingCheckInputIsRejected — доказательство Б,
// с последствием. Обязательная проверка (`go test`) читает dist/flag и на
// этапе tester проходит с flag=good. Следом verifier (`mutation: none`)
// переписывает dist/flag на bad. Раньше verified_workspace_digest не
// покрывал dist/, план предъявлялся человеку, а `go test` на том же
// candidate падал. Теперь попытка обязана быть отклонена.
func TestE2E_ReadOnlyStageInvalidatingCheckInputIsRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)
	seedFlagFixture(t, dir)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}

	code, out := runAI(t, bin, dir, []string{
		pathEnv,
		"MOCK_EVIL_AGENT=verifier",
		"MOCK_EVIL_FILES=dist/flag=bad",
		"AI_TEAM_OPENCODE_ENV_ALLOW=MOCK_EVIL_AGENT,MOCK_EVIL_FILES",
	}, "run", "--feature", "qs03-b", "--task", "QS-03 check input", "--approve-gates")

	t.Logf("=== ai-team run (сценарий Б) → exit %d\n%s", code, out)

	// Независимая проверка: на том candidate, который предъявлен человеку,
	// зелёная required-проверка больше не зелёная.
	goTest := exec.Command("go", "test", "./...")
	goTest.Dir = candidateWorktree(t, dir)
	goTestOut, goTestErr := goTest.CombinedOutput()
	t.Logf("=== go test ./... на том же состоянии → err=%v\n%s", goTestErr, goTestOut)

	if code == 3 {
		t.Fatalf("verifier сломал вход обязательной проверки, но план предъявлен "+
			"человеку (exit 3); go test на том же состоянии: err=%v\n%s\n--- run:\n%s",
			goTestErr, goTestOut, out)
	}
	if !strings.Contains(out, "нарушил mutation policy") {
		t.Fatalf("guard не заметил, что verifier изменил dist/flag (exit %d):\n%s", code, out)
	}
	manifest := stageManifest(t, dir, "verifier")
	t.Logf("=== манифест попытки verifier: status=%q mutations=%v", manifest.Status, manifest.Mutations)
	if manifest.Status != "failed" {
		t.Errorf("манифест verifier обязан быть failed, получено %q", manifest.Status)
	}
	found := false
	for _, path := range manifest.Mutations {
		if path == "dist/flag" {
			found = true
		}
	}
	if !found {
		t.Fatalf("изменённый вход проверки не атрибутирован как мутация: %v", manifest.Mutations)
	}
}

// seedFlagFixture кладёт в repo обязательную проверку, вход которой лежит в
// каталоге из прежнего ignore-набора: go-тест читает dist/flag.
func seedFlagFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "flag"), []byte("good\n"), 0644); err != nil {
		t.Fatal(err)
	}
	flagTest := `package e2eimplementation

import (
	"os"
	"strings"
	"testing"
)

func TestFlag(t *testing.T) {
	data, err := os.ReadFile("dist/flag")
	if err != nil {
		t.Fatalf("read dist/flag: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "good" {
		t.Fatalf("dist/flag says %s", got)
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "flag_test.go"), []byte(flagTest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flag.go"), []byte("package e2eimplementation\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("add", "dist/flag", "flag_test.go", "flag.go")
	runGit("commit", "-m", "flag fixture")
	runGit("push", "origin", "main")
}

// candidateWorktree возвращает isolated candidate root run'а — именно то
// состояние, из которого собран предъявленный человеку delivery plan.
func candidateWorktree(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".ai-team", "worktrees", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("candidate worktree не найден: %v %v", err, matches)
	}
	return matches[len(matches)-1]
}

// attemptManifest — та часть манифеста попытки, по которой судят об
// атрибуции мутаций.
type attemptManifest struct {
	Status    string   `json:"status"`
	Mutations []string `json:"mutations"`
}

// stageManifest читает immutable манифест последней попытки этапа.
func stageManifest(t *testing.T, dir, stage string) attemptManifest {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".ai-team", "runs", "*", "attempts", "*-"+stage, "manifest.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("манифест попытки %s не найден: %v %v", stage, err, matches)
	}
	sort.Strings(matches)
	data, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatalf("манифест попытки %s не читается: %v", stage, err)
	}
	var manifest attemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("манифест попытки %s не разбирается: %v", stage, err)
	}
	return manifest
}
