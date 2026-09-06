package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
)

// TestAI_TeamCLIReexec — точка повторного входа для subprocess-тестов
// machine-output (AUD-19): в дочернем процессе (AI_TEAM_CLI_CHILD=1)
// пересобирает os.Args из AI_TEAM_CLI_ARGS и исполняет настоящий main(),
// чтобы захватить реальный exit code и stdout (JSONL) CLI-команды.
func TestAI_TeamCLIReexec(t *testing.T) {
	if os.Getenv("AI_TEAM_CLI_CHILD") != "1" {
		return
	}
	os.Args = append([]string{"ai-team"}, strings.Fields(os.Getenv("AI_TEAM_CLI_ARGS"))...)
	main()
	os.Exit(0)
}

// runCLI запускает настоящий CLI в дочернем процессе через re-exec и
// возвращает stdout, exit code и stderr.
func runCLI(t *testing.T, args ...string) (string, int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestAI_TeamCLIReexec$")
	command.Env = append(os.Environ(),
		"AI_TEAM_CLI_CHILD=1", "AI_TEAM_CLI_ARGS="+strings.Join(args, " "))
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("child CLI: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String(), exitCode, stderr.String()
}

// parseJSONL проверяет, что stdout commands является чистым JSONL (каждая
// строка парсится как JSON, пустых/human строк нет) и возвращает records.
func parseJSONL(t *testing.T, stdout string) []logging.Record {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		t.Fatalf("JSONL пуст: machine-mode команда не выдала запись")
	}
	var records []logging.Record
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("строк %d: пустая строка в machine output (human text примешан к JSONL)", index+1)
		}
		var record logging.Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("строка %d JSONL не парсится как JSON: %v — %q", index+1, err, line)
		}
		records = append(records, record)
	}
	return records
}

// newGateRepo создаёт git-репозиторий с одним коммитом и чистым деревом —
// детерминированный PASS-вердикт для `ai-team gate`.
func newGateRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitTest(t, dir, "init", "-b", "main")
	runGitTest(t, dir, "config", "user.email", "gate@example.test")
	runGitTest(t, dir, "config", "user.name", "Gate Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "README.md")
	runGitTest(t, dir, "commit", "-m", "initial")
	return dir
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitTest(t, dir, "init", "-b", "main")
	runGitTest(t, dir, "config", "user.email", "test@example.com")
	runGitTest(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "README.md")
	runGitTest(t, dir, "commit", "-m", "initial")
	return dir
}

func writeED25519Key(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(t.TempDir(), "ed25519.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGateJSONSuccessIsSingleParseableLine — AUD-19: PASS-путь gate в JSON
// mode даёт одну чистую JSONL-запись (без stray human text, в т.ч. без
// бывшего fmt.Println в printGateSummary) и exit 0.
func TestGateJSONSuccessIsSingleParseableLine(t *testing.T) {
	dir := newGateRepo(t)
	out := filepath.Join(t.TempDir(), "bundle")
	stdout, code, stderr := runCLI(t, "gate", "--json", "--target", dir, "--out", out)
	if code != 0 {
		t.Fatalf("ожидался exit 0 (PASS), получен %d; stderr: %s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 {
		t.Fatalf("ожидалась одна JSONL-запись, получено %d: %q", len(records), stdout)
	}
	if records[0].Command != "gate" || records[0].Exit != 0 {
		t.Fatalf("gate record: cmd=%q exit=%d, получено %v", records[0].Command, records[0].Exit, records[0])
	}
	if signed, ok := records[0].Data["signed"].(bool); !ok || signed {
		t.Fatalf("signed без --sign-key должен быть false, получено %v (ok=%v)", records[0].Data["signed"], ok)
	}
}

// TestGateJSONInvalidConfigExitsBlocked — AUD-19: malformed gate config
// (неизвестный adapter) даёт exit 2 (BLOCKED), а не exit 1 через fatal, и
// в JSON mode stdout остаётся чистым JSONL с exit_code 2.
func TestGateJSONInvalidConfigExitsBlocked(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "bad-gate.yaml")
	bad := "schema_version: 1\ndiff_policy:\n  test_modify: required\nchecks:\n  - name: mystery\n    class: unit\n    adapter: mystery-runner\n    command: [go, test, -json, ./...]\n    policy: required\n"
	if err := os.WriteFile(configPath, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, code, stderr := runCLI(t, "gate", "--json", "--target", dir, "--config", configPath)
	if code != exitBlocked {
		t.Fatalf("невалидный gate config должен дать exit 2 (BLOCKED), получен %d; stderr: %s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 {
		t.Fatalf("ожидалась одна JSONL-запись, получено %d: %q", len(records), stdout)
	}
	if records[0].Exit != exitBlocked || records[0].Type != "gate_config" {
		t.Fatalf("gate_config record: exit=%d type=%q, получено %v", records[0].Exit, records[0].Type, records[0])
	}
	if !strings.Contains(records[0].Message, "adapter") {
		t.Fatalf("диагностика должна называть неизвестный adapter: %q", records[0].Message)
	}
}

// TestGateJSONBlockedEmitsStructuredRecord — AUD-19: BLOCKED-путь gate
// (неразрешимый local ref = «нужно вмешательство») в JSON mode даёт exit 2 и
// структурированную JSONL-запись, а не только human text.
func TestGateJSONBlockedEmitsStructuredRecord(t *testing.T) {
	dir := t.TempDir()
	runGitTest(t, dir, "init", "-b", "main")
	out := filepath.Join(t.TempDir(), "bundle")
	stdout, code, stderr := runCLI(t, "gate", "--json", "--target", dir, "--out", out)
	if code != exitBlocked {
		t.Fatalf("пустой repo должен дать exit 2 (BLOCKED), получен %d; stderr: %s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 {
		t.Fatalf("ожидалась одна JSONL-запись, получено %d: %q", len(records), stdout)
	}
	if records[0].Exit != exitBlocked || records[0].Command != "gate" {
		t.Fatalf("blocked gate record: cmd=%q exit=%d, получено %v", records[0].Command, records[0].Exit, records[0])
	}
}

// TestGateJSONSignedReportsTrue — AUD-19: при реальной DSSE-подписи
// (--sign-key) JSONL-запись gate содержит signed:true, а не literal false.
func TestGateJSONSignedReportsTrue(t *testing.T) {
	dir := newGateRepo(t)
	keyPath := writeED25519Key(t)
	out := filepath.Join(t.TempDir(), "bundle")
	stdout, code, stderr := runCLI(t, "gate", "--json", "--target", dir, "--out", out, "--sign-key", keyPath)
	if code != 0 {
		t.Fatalf("ожидался exit 0 (PASS), получен %d; stderr: %s", code, stderr)
	}
	records := parseJSONL(t, stdout)
	if len(records) != 1 {
		t.Fatalf("ожидалась одна JSONL-запись, получено %d: %q", len(records), stdout)
	}
	signed, ok := records[0].Data["signed"].(bool)
	if !ok || !signed {
		t.Fatalf("--sign-key должен давать signed:true, получено %v (ok=%v)", records[0].Data["signed"], ok)
	}
	if _, err := os.Stat(filepath.Join(out, "dsse.json")); err != nil {
		t.Fatalf("подписанный bundle должен содержать dsse.json: %v", err)
	}
}

// TestRunPreflightClassifiesMissingDeliveryPrereqs — AUD-09: CLI-путь
// (runPreflight в cmdRun) ходит в тот же pkg/preflight, что web-контроллер
// (control.WithPreflight) и worker; классификация отсутствующих
// runtime/gh/origin идентична во всех entry points. Выбранное различие
// (зафиксировано в README, "Контроллер и preflight"): CLI показывает отчёт
// read-only и блокирует только невозможность запустить runtime вовсе (cli);
// delivery-предусловия fail-closed проверяются на самой delivery-стадии.
func TestRunPreflightClassifiesMissingDeliveryPrereqs(t *testing.T) {
	dir := newGitRepo(t)
	registry := agent.NewFS(fstest.MapFS{
		"ship/def.yaml": &fstest.MapFile{Data: []byte("name: ship\nkind: delivery\nmutation: external\nruntime: delivery\ninputs:\n  review: review.md\noutputs:\n  plan: plan.json\npreconditions:\n  review:\n    required: true\n    marker: Verdict\n    values: [APPROVED]\n")},
	})
	cfg := &config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: "ship"}}}
	// Read-only: CLI не блокирует run из-за отсутствующих delivery-предусловий.
	if err := runPreflight(cfg, registry, dir); err != nil {
		t.Fatalf("CLI preflight read-only не должен блокировать delivery workflow без origin, получено: %v", err)
	}
	// Но классификация та же, что у web/worker: delivery_remote помечен как failed+required.
	report := preflight.New(cfg, registry, dir).Check(context.Background())
	var classified bool
	for _, check := range report.Checks {
		if check.ID == "delivery_remote" {
			classified = true
			if !check.Required || check.Status != preflight.StatusFailed {
				t.Fatalf("delivery_remote должен быть required+failed, получено: %+v", check)
			}
		}
	}
	if !classified {
		t.Fatal("preflight должен классифицировать missing origin как delivery_remote")
	}
}

// TestRunPreflightBlocksWithoutRuntime — AUD-09: CLI жёстко блокирует run,
// только когда runtime в принципе не может стартовать (cli check failed).
func TestRunPreflightBlocksWithoutRuntime(t *testing.T) {
	dir := newGitRepo(t)
	registry := agent.NewFS(fstest.MapFS{
		"worker/def.yaml": &fstest.MapFile{Data: []byte("name: worker\nruntime: agentcli\nmutation: none\n")},
	})
	cfg := &config.Config{CLI: "no-such-cli-binary", PipelineAgents: []config.AgentConfig{{Name: "worker"}}}
	err := runPreflight(cfg, registry, dir)
	if err == nil {
		t.Fatal("CLI preflight должен блокировать run без доступного runtime (cli check)")
	}
	if !strings.Contains(err.Error(), "cli") {
		t.Fatalf("CLI preflight должен ссылаться на cli check, получено: %v", err)
	}
}

// TestRunPreflightSkipsDeliveryChecksWithoutDeliveryStage — AUD-09: без
// delivery-стадии CLI preflight не требует origin/gh (классификация та же,
// что у web/worker — общий pkg/preflight).
func TestRunPreflightSkipsDeliveryChecksWithoutDeliveryStage(t *testing.T) {
	dir := newGitRepo(t)
	registry := agent.NewFS(fstest.MapFS{
		"worker/def.yaml": &fstest.MapFile{Data: []byte("name: worker\nruntime: agentcli\nmutation: none\n")},
	})
	cfg := &config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: "worker"}}}
	report := preflight.New(cfg, registry, dir).Check(context.Background())
	for _, check := range report.Checks {
		if check.ID == "delivery_remote" || check.ID == "github_auth" {
			t.Fatalf("без delivery-стадии preflight не должен требовать origin/gh, получено: %+v", check)
		}
	}
	if err := runPreflight(cfg, registry, dir); err != nil {
		t.Fatalf("без delivery-стадии CLI preflight не должен блокировать run, получено: %v", err)
	}
}
