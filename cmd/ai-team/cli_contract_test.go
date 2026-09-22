package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/worker"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// cli_contract_test.go — issue #103: разбор аргументов, exit-коды и отказ на
// отсутствующих/конфликтующих флагах для всех подкоманд CLI. Всё, что здесь
// проверяется, — пользовательский контракт `ai-team`, зафиксированный в
// openspec/specs/cli-interface: сообщение и код возврата, а не внутренности.
//
// Тесты гоняют настоящий бинарь в дочернем процессе (runCLI из main_test.go),
// поэтому проверяется реальный exit code, а не возврат функции.

// newControlRoot — target с инициализированным (и безопасным) .ai-team.
func newControlRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runCLIStdin — как runCLI, но подаёт команде stdin (нужно для `worker`,
// который читает job из stdin).
func runCLIStdin(t *testing.T, stdin string, args ...string) (string, int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestAI_TeamCLIReexec$")
	command.Env = append(os.Environ(),
		"AI_TEAM_CLI_CHILD=1", "AI_TEAM_CLI_ARGS="+strings.Join(args, " "))
	command.Stdin = strings.NewReader(stdin)
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

// TestCLIDispatchContract — главный switch main(): неизвестная команда и
// пустой вызов обязаны завершаться ненулевым кодом со справкой, а `version`
// и `help` — нулевым.
func TestCLIDispatchContract(t *testing.T) {
	t.Run("неизвестная команда", func(t *testing.T) {
		stdout, code, stderr := runCLI(t, "definitely-not-a-command")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "Неизвестная команда") ||
			!strings.Contains(stderr, "definitely-not-a-command") {
			t.Fatalf("диагностика должна называть неизвестную команду, получено: %q", stderr)
		}
		if !strings.Contains(stdout, "ai-team init") {
			t.Fatalf("после неизвестной команды должна печататься справка:\n%s", stdout)
		}
	})

	t.Run("без аргументов", func(t *testing.T) {
		stdout, code, _ := runCLI(t)
		if code != 1 {
			t.Fatalf("вызов без подкоманды должен давать exit 1, получен %d", code)
		}
		if !strings.Contains(stdout, "Использование:") {
			t.Fatalf("ожидалась справка, получено:\n%s", stdout)
		}
	})

	t.Run("version", func(t *testing.T) {
		stdout, code, stderr := runCLI(t, "version")
		if code != 0 {
			t.Fatalf("version должен давать exit 0, получен %d; stderr: %s", code, stderr)
		}
		if strings.TrimSpace(stdout) == "" {
			t.Fatal("version обязан печатать версию")
		}
	})

	t.Run("help", func(t *testing.T) {
		for _, arg := range []string{"help", "--help", "-h"} {
			stdout, code, stderr := runCLI(t, arg)
			if code != 0 {
				t.Fatalf("%s должен давать exit 0, получен %d; stderr: %s", arg, code, stderr)
			}
			// Справка обязана перечислять подкоманды: она — единственный
			// источник контракта для пользователя без документации.
			for _, command := range []string{"ai-team run", "ai-team gate", "ai-team verify", "ai-team export"} {
				if !strings.Contains(stdout, command) {
					t.Fatalf("%s: справка не упоминает %q:\n%s", arg, command, stdout)
				}
			}
		}
	})
}

// TestRunFlagContract — `run` обязан отказывать до любой дорогой работы:
// на неинициализированном target, без --feature, на недопустимом имени фичи
// и на конфликте --resume с --feature/--task.
func TestRunFlagContract(t *testing.T) {
	t.Run("неинициализированный target", func(t *testing.T) {
		_, code, stderr := runCLI(t, "run", "--target", t.TempDir(), "--feature", "x", "--task", "y")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "не инициализирован") {
			t.Fatalf("ожидалась диагностика про инициализацию, получено: %q", stderr)
		}
	})

	t.Run("без --feature", func(t *testing.T) {
		_, code, stderr := runCLI(t, "run", "--target", newControlRoot(t))
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "--feature") {
			t.Fatalf("диагностика должна требовать --feature, получено: %q", stderr)
		}
	})

	t.Run("недопустимое имя фичи", func(t *testing.T) {
		_, code, stderr := runCLI(t, "run", "--target", newControlRoot(t), "--feature", "../escape", "--task", "t")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "недопустимое имя фичи") {
			t.Fatalf("path traversal в --feature обязан отклоняться, получено: %q", stderr)
		}
	})

	t.Run("--resume конфликтует с --feature", func(t *testing.T) {
		root := newControlRoot(t)
		for _, extra := range [][]string{{"--feature", "f"}, {"--task", "t"}} {
			args := append([]string{"run", "--target", root, "--resume", "run-1"}, extra...)
			_, code, stderr := runCLI(t, args...)
			if code != 1 {
				t.Fatalf("%v: ожидался exit 1, получен %d; stderr: %s", extra, code, stderr)
			}
			if !strings.Contains(stderr, "нельзя сочетать") {
				t.Fatalf("%v: ожидалась диагностика конфликта флагов, получено: %q", extra, stderr)
			}
		}
	})
}

// TestExitCodeForMapsRunErrors — exit-коды run зафиксированы в спеке
// cli-interface (0/1/2/3) и читаются скриптами; отображение ошибки в код —
// часть контракта, а не деталь реализации.
func TestExitCodeForMapsRunErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected int
	}{
		{"nil", nil, exitOK},
		{"blocked outcome", &pipeline.RunError{Outcome: workflow.RunOutcome("blocked"), Err: errors.New("x")}, exitBlocked},
		{"stopped outcome", &pipeline.RunError{Outcome: workflow.RunOutcome("stopped"), Err: errors.New("x")}, exitUserStopped},
		{"failed outcome", &pipeline.RunError{Outcome: workflow.RunOutcome("failed"), Err: errors.New("x")}, exitFailed},
		{"blocked error", &pipeline.BlockedError{Agent: "coder", Reason: "нет спеки"}, exitBlocked},
		{"user stopped", pipeline.ErrUserStopped, exitUserStopped},
		{"wrapped user stopped", fmt.Errorf("обёртка: %w", pipeline.ErrUserStopped), exitUserStopped},
		{"прочая ошибка", errors.New("boom"), exitFailed},
	}
	for _, testCase := range cases {
		if actual := exitCodeFor(testCase.err); actual != testCase.expected {
			t.Fatalf("%s: exitCodeFor = %d, ожидалось %d", testCase.name, actual, testCase.expected)
		}
	}
}

// TestWorkerOutcomeForSeparatesBusinessFromInfra — контракт worker-протокола:
// durable business-исход (blocked/stopped/canceled/waiting_for_approval) не
// должен перезапускаться планировщиком, инфраструктурный сбой — должен.
func TestWorkerOutcomeForSeparatesBusinessFromInfra(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected string
	}{
		{"blocked", &pipeline.RunError{Outcome: workflow.RunOutcome("blocked"), Err: errors.New("x")}, worker.OutcomeBlocked},
		{"stopped", &pipeline.RunError{Outcome: workflow.RunOutcome("stopped"), Err: errors.New("x")}, worker.OutcomeStopped},
		{"canceled", &pipeline.RunError{Outcome: workflow.RunOutcome("canceled"), Err: errors.New("x")}, worker.OutcomeCanceled},
		{"failed", &pipeline.RunError{Outcome: workflow.RunOutcome("failed"), Err: errors.New("x")}, worker.OutcomeFailed},
		{"user stopped", pipeline.ErrUserStopped, worker.OutcomeStopped},
		// ApprovalRequiredError.Unwrap() отдаёт ErrUserStopped, поэтому
		// ветка errors.Is(ErrUserStopped) срабатывает раньше явной проверки
		// approval: исход классифицируется как stopped. Для планировщика
		// разницы нет (оба controlled — job не перезапускается), но
		// worker.OutcomeWaitingApproval из этой функции недостижим.
		{"approval", &pipeline.ApprovalRequiredError{Checkpoint: "delivery"}, worker.OutcomeStopped},
		{"инфраструктурный сбой", errors.New("controller crash"), worker.OutcomeInfraFailed},
	}
	for _, testCase := range cases {
		actual := workerOutcomeFor(testCase.err)
		if actual != testCase.expected {
			t.Fatalf("%s: workerOutcomeFor = %q, ожидалось %q", testCase.name, actual, testCase.expected)
		}
		controlled := worker.Result{Outcome: actual}.Controlled()
		if testCase.expected == worker.OutcomeInfraFailed || testCase.expected == worker.OutcomeFailed {
			if controlled {
				t.Fatalf("%s: исход %q не должен считаться controlled", testCase.name, actual)
			}
		} else if !controlled {
			t.Fatalf("%s: durable исход %q должен считаться controlled", testCase.name, actual)
		}
	}
}

// TestDecisionFlagContract — `decision` пишет решение человека; неполный
// набор идентифицирующих флагов обязан отклоняться, а не записывать решение
// с пустым actor/subject.
func TestDecisionFlagContract(t *testing.T) {
	t.Run("без обязательных флагов", func(t *testing.T) {
		_, code, stderr := runCLI(t, "decision")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		for _, flagName := range []string{"--run", "--approval", "--actor", "--role", "--action", "--subject"} {
			if !strings.Contains(stderr, flagName) {
				t.Fatalf("диагностика должна перечислять %s, получено: %q", flagName, stderr)
			}
		}
	})

	t.Run("позиционный аргумент", func(t *testing.T) {
		_, code, stderr := runCLI(t, "decision", "approve")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Неожиданные аргументы decision") {
			t.Fatalf("позиционный аргумент должен отклоняться, получено: %q", stderr)
		}
	})

	t.Run("контроль root проверяется до записи", func(t *testing.T) {
		target := t.TempDir()
		_, code, stderr := runCLI(t, "decision", "--target", target,
			"--run", "run-1", "--approval", "ap-1", "--actor", "human",
			"--role", "release_manager", "--action", "approve", "--subject", strings.Repeat("a", 64))
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "не инициализирован") {
			t.Fatalf("ожидался отказ по control root, получено: %q", stderr)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Fatalf("отклонённое решение не должно ничего создавать в target: %v", entries)
		}
	})
}

// TestUsageCommandContract — `usage <run_id>` печатает сводку завершённого
// run; аргумент обязателен, run_id не должен выводить за .ai-team/runs, а
// повреждённый usage.json обязан отклоняться, а не печатать мусор.
func TestUsageCommandContract(t *testing.T) {
	root := newControlRoot(t)

	t.Run("без run_id", func(t *testing.T) {
		_, code, stderr := runCLI(t, "usage", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "ai-team usage") {
			t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
		}
	})

	t.Run("run_id с путём отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "usage", "--target", root, "../../etc")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Некорректный run_id") {
			t.Fatalf("path traversal в run_id должен отклоняться, получено: %q", stderr)
		}
	})

	t.Run("отсутствующий run", func(t *testing.T) {
		_, code, stderr := runCLI(t, "usage", "--target", root, "no-such-run")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Не удалось прочитать usage") {
			t.Fatalf("ожидался отказ чтения usage, получено: %q", stderr)
		}
	})

	t.Run("повреждённый usage.json", func(t *testing.T) {
		runDir := filepath.Join(root, ".ai-team", "runs", "broken")
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Неизвестное поле: декодер строгий, и это намеренно — usage.json
		// чужой схемы не должен молча печататься как своя.
		if err := os.WriteFile(filepath.Join(runDir, "usage.json"), []byte(`{"unexpected":1}`), 0644); err != nil {
			t.Fatal(err)
		}
		_, code, stderr := runCLI(t, "usage", "--target", root, "broken")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Повреждённый usage.json") {
			t.Fatalf("ожидался отказ по схеме, получено: %q", stderr)
		}
	})

	t.Run("валидный usage", func(t *testing.T) {
		runDir := filepath.Join(root, ".ai-team", "runs", "good")
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		envelope := metrics.UsageEnvelope{
			SchemaVersion:   metrics.SchemaVersion,
			RunID:           "good",
			Feature:         "fixture-feature",
			StartedAt:       started,
			FinishedAt:      started.Add(90 * time.Second),
			TotalDurationMS: 90000,
			Stages:          []metrics.StageMetrics{{Stage: "coder", Attempts: 2, DurationMS: 60000}},
			LoopbackCycles:  1,
			TokensUnknown:   true,
			Outcome:         "completed",
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "usage.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		stdout, code, stderr := runCLI(t, "usage", "--target", root, "good")
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		for _, expected := range []string{"fixture-feature", "completed", "coder", "Loopback: 1"} {
			if !strings.Contains(stdout, expected) {
				t.Fatalf("usage-сводка должна содержать %q:\n%s", expected, stdout)
			}
		}
	})
}

// writeED25519PublicKey кладёт raw ed25519 public key на диск.
func writeED25519PublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "verify.key")
	if err := os.WriteFile(path, public, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestVerifyCommandContract — `verify` принимает ровно один аргумент
// (run_id или каталог bundle); --verify-key относится только к bundle и на
// ветке live evidence обязан отказывать fail-closed, а не молча
// игнорироваться (иначе пользователь считает подпись проверенной).
func TestVerifyCommandContract(t *testing.T) {
	root := newControlRoot(t)

	t.Run("без аргумента", func(t *testing.T) {
		_, code, stderr := runCLI(t, "verify", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "ai-team verify") {
			t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
		}
	})

	t.Run("несуществующий bundle-каталог", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "bundle-dir")
		_, code, stderr := runCLI(t, "verify", missing)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "не найден") {
			t.Fatalf("ожидалась диагностика отсутствующего bundle, получено: %q", stderr)
		}
	})

	t.Run("нечитаемый verify-key", func(t *testing.T) {
		_, code, stderr := runCLI(t, "verify", "--target", root,
			"--verify-key", filepath.Join(t.TempDir(), "absent.pem"), "some-run")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "verify key") {
			t.Fatalf("ожидался отказ загрузки ключа, получено: %q", stderr)
		}
	})

	t.Run("--verify-key на live evidence отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "verify", "--target", root,
			"--verify-key", writeED25519PublicKey(t), "some-run")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "DSSE") || !strings.Contains(stderr, "bundle") {
			t.Fatalf("отказ должен объяснять, что подпись применима только к bundle, получено: %q", stderr)
		}
	})

	t.Run("отсутствующая evidence run", func(t *testing.T) {
		_, code, stderr := runCLI(t, "verify", "--target", root, "no-such-run")
		if code != exitFailed {
			t.Fatalf("ожидался exit %d, получен %d; stderr: %s", exitFailed, code, stderr)
		}
		if !strings.Contains(stderr, "no-such-run") {
			t.Fatalf("диагностика должна называть run, получено: %q", stderr)
		}
	})
}

// TestGateBundleVerifyRoundTrip — `gate` без --out кладёт attestation bundle
// в .ai-team/gates/<ts>, а `verify <bundle-dir>` обязан отличить gate bundle
// от run bundle и проверить его. Подпись: без --verify-key verify честно
// сообщает, что подпись не проверялась; с ключом — проверяет её fail-closed.
func TestGateBundleVerifyRoundTrip(t *testing.T) {
	repo := newGateRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	keyPath := writeED25519Key(t)

	if _, code, stderr := runCLI(t, "gate", "--target", repo, "--sign-key", keyPath); code != 0 {
		t.Fatalf("gate: ожидался exit 0 (PASS), получен %d; stderr: %s", code, stderr)
	}
	// --out не задан: bundle обязан лечь в control root, а не в CWD.
	gatesDir := filepath.Join(repo, ".ai-team", "gates")
	entries, err := os.ReadDir(gatesDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("gate без --out обязан публиковать bundle в %s: %v (%d записей)", gatesDir, err, len(entries))
	}
	bundle := filepath.Join(gatesDir, entries[0].Name())
	if _, err := os.Stat(filepath.Join(bundle, "gate.json")); err != nil {
		t.Fatalf("gate bundle обязан содержать gate.json: %v", err)
	}

	t.Run("без ключа подпись не считается проверенной", func(t *testing.T) {
		stdout, code, stderr := runCLI(t, "verify", bundle)
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "Gate bundle") {
			t.Fatalf("verify обязан распознать gate bundle:\n%s", stdout)
		}
		if !strings.Contains(stdout, "без верификации подписи") {
			t.Fatalf("без --verify-key verify обязан сообщать, что подпись не проверена:\n%s", stdout)
		}
	})

	t.Run("верный ключ подтверждает подпись", func(t *testing.T) {
		publicPath := filepath.Join(t.TempDir(), "gate.pub")
		private, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(private)
		if block == nil {
			t.Fatal("ожидался PEM private key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		signer, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			t.Fatalf("ожидался ed25519 key, получен %T", parsed)
		}
		if err := os.WriteFile(publicPath, signer.Public().(ed25519.PublicKey), 0600); err != nil {
			t.Fatal(err)
		}
		stdout, code, stderr := runCLI(t, "verify", "--verify-key", publicPath, bundle)
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "подпись DSSE ed25519 подтверждена") {
			t.Fatalf("verify обязан подтвердить подпись:\n%s", stdout)
		}
	})

	t.Run("чужой ключ отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "verify", "--verify-key", writeED25519PublicKey(t), bundle)
		if code != exitFailed {
			t.Fatalf("чужой ключ обязан давать exit %d, получен %d; stderr: %s", exitFailed, code, stderr)
		}
	})
}

// TestExportCommandContract — `export` принимает ровно один run_id, не
// экспортирует нетерминальный run и не принимает нечитаемый signing key.
func TestExportCommandContract(t *testing.T) {
	root := newControlRoot(t)

	t.Run("без run_id", func(t *testing.T) {
		_, code, stderr := runCLI(t, "export", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "ai-team export") {
			t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
		}
	})

	t.Run("нечитаемый sign-key", func(t *testing.T) {
		_, code, stderr := runCLI(t, "export", "--target", root,
			"--sign-key", filepath.Join(t.TempDir(), "absent.pem"), "run-1")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "signing key") {
			t.Fatalf("ожидался отказ загрузки ключа, получено: %q", stderr)
		}
	})

	t.Run("run_id с путём отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "export", "--target", root, "../../etc")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "недопустимый run_id") {
			t.Fatalf("path traversal в run_id должен отклоняться, получено: %q", stderr)
		}
	})

	t.Run("нетерминальный run", func(t *testing.T) {
		runDir := filepath.Join(root, ".ai-team", "runs", "live-run")
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		_, code, stderr := runCLI(t, "export", "--target", root, "live-run")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "не является терминальным") {
			t.Fatalf("run без anchor.json не должен экспортироваться, получено: %q", stderr)
		}
	})
}

// TestDeliverCommandContract — `deliver` повторяет отложенную доставку; без
// --run команда бессмысленна, а run_id с разделителем пути — попытка выйти
// за .ai-team/runs.
func TestDeliverCommandContract(t *testing.T) {
	root := newControlRoot(t)

	t.Run("без --run", func(t *testing.T) {
		_, code, stderr := runCLI(t, "deliver", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "ai-team deliver") {
			t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
		}
	})

	t.Run("run_id с путём отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "deliver", "--target", root, "--run", "../escape")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "недопустимый run_id") {
			t.Fatalf("ожидался отказ по run_id, получено: %q", stderr)
		}
	})

	t.Run("отсутствующий deferred run", func(t *testing.T) {
		_, code, stderr := runCLI(t, "deliver", "--target", root, "--run", "no-such-run")
		if code != exitFailed {
			t.Fatalf("ожидался exit %d, получен %d; stderr: %s", exitFailed, code, stderr)
		}
		if !strings.Contains(stderr, "no-such-run") {
			t.Fatalf("диагностика должна называть run, получено: %q", stderr)
		}
	})
}

// TestGCCommandContract — `gc` мутирует control-каталог: позиционные
// аргументы и отрицательный --keep-last обязаны отклоняться, а --dry-run
// обязан печатать план и ничего не удалять.
func TestGCCommandContract(t *testing.T) {
	t.Run("позиционный аргумент", func(t *testing.T) {
		_, code, stderr := runCLI(t, "gc", "--target", newControlRoot(t), "runs")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Неожиданные аргументы gc") {
			t.Fatalf("позиционный аргумент должен отклоняться, получено: %q", stderr)
		}
	})

	t.Run("отрицательный --keep-last", func(t *testing.T) {
		_, code, stderr := runCLI(t, "gc", "--target", newControlRoot(t), "--keep-last", "-1")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "--keep-last") {
			t.Fatalf("ожидался отказ по --keep-last, получено: %q", stderr)
		}
	})

	t.Run("dry-run ничего не удаляет", func(t *testing.T) {
		root := newControlRoot(t)
		// Старый terminal run: в plan он попадает только вместе с anchor.json.
		runDir := filepath.Join(root, ".ai-team", "runs", "old-run")
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "anchor.json"), []byte("{}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		stdout, code, stderr := runCLI(t, "gc", "--target", root, "--dry-run", "--keep-last", "0", "--older-than", "1ns")
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "dry-run: ничего не удалено") {
			t.Fatalf("dry-run обязан сообщать, что ничего не удалено:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Итого") {
			t.Fatalf("dry-run обязан печатать план:\n%s", stdout)
		}
		if _, err := os.Stat(filepath.Join(runDir, "anchor.json")); err != nil {
			t.Fatalf("dry-run удалил evidence: %v", err)
		}
	})
}

// TestRedactCommandContract — P1-6 privacy-контракт: подкоманда обязательна,
// --run/--path не должны выводить за корень сканирования, а scan/verify/redact
// обязаны действительно находить и заменять секрет.
func TestRedactCommandContract(t *testing.T) {
	secret := "ghp_" + strings.Repeat("7", 36)

	newRootWithSecret := func(t *testing.T) string {
		t.Helper()
		root := newControlRoot(t)
		runDir := filepath.Join(root, ".ai-team", "runs", "leaky")
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "stdout.log"),
			[]byte("GITHUB_TOKEN="+secret+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("без подкоманды", func(t *testing.T) {
		_, code, stderr := runCLI(t, "redact", "--target", newControlRoot(t))
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "verify") || !strings.Contains(stderr, "scan") {
			t.Fatalf("ожидалась подсказка по подкомандам, получено: %q", stderr)
		}
	})

	t.Run("неизвестная подкоманда", func(t *testing.T) {
		_, code, stderr := runCLI(t, "redact", "wipe", "--target", newControlRoot(t))
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "ai-team redact") {
			t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
		}
	})

	t.Run("--run с путём отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "redact", "scan", "--target", newControlRoot(t), "--run", "../escape")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "недопустимый run_id") {
			t.Fatalf("ожидался отказ по run_id, получено: %q", stderr)
		}
	})

	t.Run("--path за пределами корня отклоняется", func(t *testing.T) {
		_, code, stderr := runCLI(t, "redact", "scan", "--target", newControlRoot(t), "--path", "../../etc")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "выходит за пределы") {
			t.Fatalf("ожидался отказ по --path, получено: %q", stderr)
		}
	})

	t.Run("scan находит секрет", func(t *testing.T) {
		stdout, code, stderr := runCLI(t, "redact", "scan", "--target", newRootWithSecret(t))
		if code != 0 {
			t.Fatalf("scan не блокирует, ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "stdout.log") {
			t.Fatalf("scan обязан назвать файл с находкой:\n%s", stdout)
		}
	})

	t.Run("verify блокирует", func(t *testing.T) {
		_, code, stderr := runCLI(t, "redact", "verify", "--target", newRootWithSecret(t))
		if code != exitFailed {
			t.Fatalf("verify с секретом обязан давать exit %d, получен %d", exitFailed, code)
		}
		if !strings.Contains(stderr, "Redaction") {
			t.Fatalf("ожидалась диагностика redaction, получено: %q", stderr)
		}
	})

	t.Run("redact заменяет секрет в копии", func(t *testing.T) {
		root := newRootWithSecret(t)
		out := filepath.Join(t.TempDir(), "redacted")
		stdout, code, stderr := runCLI(t, "redact", "redact", "--target", root, "--out", out)
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "Redacted") {
			t.Fatalf("ожидался отчёт о redaction:\n%s", stdout)
		}
		copied, err := os.ReadFile(filepath.Join(out, "leaky", "stdout.log"))
		if err != nil {
			t.Fatalf("redaction-копия не создана: %v", err)
		}
		if strings.Contains(string(copied), secret) {
			t.Fatalf("секрет остался в redaction-копии: %s", copied)
		}
		if !strings.Contains(string(copied), "REDACTED") {
			t.Fatalf("ожидался маркер [REDACTED:...] в копии: %s", copied)
		}
		// Оригинал неизменен: redact делает detached-копию, а не правит evidence.
		original, err := os.ReadFile(filepath.Join(root, ".ai-team", "runs", "leaky", "stdout.log"))
		if err != nil || !strings.Contains(string(original), secret) {
			t.Fatalf("redact не должен трогать исходную evidence: %q (%v)", original, err)
		}
	})

	t.Run("повторный --out отклоняется", func(t *testing.T) {
		root := newRootWithSecret(t)
		out := filepath.Join(t.TempDir(), "redacted")
		if _, code, stderr := runCLI(t, "redact", "redact", "--target", root, "--out", out); code != 0 {
			t.Fatalf("первый redact: exit %d; stderr: %s", code, stderr)
		}
		_, code, stderr := runCLI(t, "redact", "redact", "--target", root, "--out", out)
		if code != 1 {
			t.Fatalf("существующий --out должен отклоняться, получен exit %d", code)
		}
		if !strings.Contains(stderr, "уже существует") {
			t.Fatalf("ожидалась диагностика существующего каталога, получено: %q", stderr)
		}
	})
}

// TestValidRunIDRejectsPathAndGlob — run_id подставляется в путь под
// .ai-team/runs, поэтому разделители пути и glob-метасимволы недопустимы.
func TestValidRunIDRejectsPathAndGlob(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a*b", "a?b", "a[b]", "a{b}", "a:b"} {
		if validRunID(bad) {
			t.Fatalf("run_id %q должен отклоняться", bad)
		}
	}
	for _, good := range []string{"run-1", "20260101T000000Z-feature", "run_1.2"} {
		if !validRunID(good) {
			t.Fatalf("run_id %q должен приниматься", good)
		}
	}
}

// TestAuthTokenContract — `auth-token` выпускает короткоживущий cloud token;
// без идентичности выпускать нечего, неизвестная роль обязана отклоняться, а
// выпущенный token обязан проверяться тем же secret.
func TestAuthTokenContract(t *testing.T) {
	t.Run("без --actor/--roles", func(t *testing.T) {
		_, code, stderr := runCLI(t, "auth-token")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "--actor") || !strings.Contains(stderr, "--roles") {
			t.Fatalf("диагностика должна называть обязательные флаги, получено: %q", stderr)
		}
	})

	t.Run("неизвестная роль", func(t *testing.T) {
		_, code, stderr := runCLI(t, "auth-token", "--actor", "human", "--roles", "wizard")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "wizard") {
			t.Fatalf("диагностика должна называть неизвестную роль, получено: %q", stderr)
		}
	})

	t.Run("короткий secret отклоняется", func(t *testing.T) {
		t.Setenv("AI_TEAM_TEST_SECRET", "too-short")
		_, code, stderr := runCLI(t, "auth-token", "--actor", "human",
			"--roles", "developer", "--secret-env", "AI_TEAM_TEST_SECRET")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "32") {
			t.Fatalf("ожидался отказ по длине secret, получено: %q", stderr)
		}
	})

	t.Run("выпущенный token верифицируется", func(t *testing.T) {
		secret := strings.Repeat("s", 48)
		t.Setenv("AI_TEAM_TEST_SECRET", secret)
		stdout, code, stderr := runCLI(t, "auth-token", "--actor", "human",
			"--roles", "developer,reviewer", "--ttl", "15m", "--secret-env", "AI_TEAM_TEST_SECRET")
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		manager, err := cloudidentity.NewTokenManager([]byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		principal, err := manager.Verify(strings.TrimSpace(stdout))
		if err != nil {
			t.Fatalf("выпущенный token не верифицируется: %v", err)
		}
		if principal.ActorID != "human" || len(principal.Roles) != 2 {
			t.Fatalf("неожиданный principal: %+v", principal)
		}
	})

	t.Run("слишком длинный ttl отклоняется", func(t *testing.T) {
		t.Setenv("AI_TEAM_TEST_SECRET", strings.Repeat("s", 48))
		_, code, stderr := runCLI(t, "auth-token", "--actor", "human",
			"--roles", "developer", "--ttl", "48h", "--secret-env", "AI_TEAM_TEST_SECRET")
		if code != 1 {
			t.Fatalf("ttl сверх максимума должен отклоняться, получен exit %d; stderr: %s", code, stderr)
		}
	})
}

// TestWorkerCommandContract — disposable worker читает job из stdin и обязан
// отказываться на невалидном job, чужом target и относительном --db.
func TestWorkerCommandContract(t *testing.T) {
	t.Run("без --target", func(t *testing.T) {
		_, code, stderr := runCLIStdin(t, "", "worker")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "--target") {
			t.Fatalf("диагностика должна требовать --target, получено: %q", stderr)
		}
	})

	t.Run("неинициализированный target", func(t *testing.T) {
		_, code, stderr := runCLIStdin(t, "", "worker", "--target", t.TempDir())
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "не инициализирован") {
			t.Fatalf("ожидался отказ по control root, получено: %q", stderr)
		}
	})

	t.Run("пустой stdin", func(t *testing.T) {
		_, code, stderr := runCLIStdin(t, "", "worker", "--target", newControlRoot(t))
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Невалидный worker job") {
			t.Fatalf("ожидался отказ по job, получено: %q", stderr)
		}
	})

	t.Run("job чужого target", func(t *testing.T) {
		root := newControlRoot(t)
		job, err := json.Marshal(worker.Job{
			SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
			RunID: "run-1", TargetDir: filepath.Clean(t.TempDir()), Feature: "f", Task: "t",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, code, stderr := runCLIStdin(t, string(job), "worker", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Невалидный worker job") {
			t.Fatalf("job чужого target обязан отклоняться, получено: %q", stderr)
		}
	})

	t.Run("относительный --db", func(t *testing.T) {
		root := newControlRoot(t)
		job, err := json.Marshal(worker.Job{
			SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
			RunID: "run-1", TargetDir: filepath.Clean(root), Feature: "f", Task: "t",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, code, stderr := runCLIStdin(t, string(job), "worker", "--target", root, "--db", "relative/web.db")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "absolute path") {
			t.Fatalf("относительный --db обязан отклоняться, получено: %q", stderr)
		}
	})
}

// TestEvalCommandContract — `eval` без пары флагов не знает, что оценивать, а
// --samples вне 1..20 — заведомо ошибка вызова.
func TestEvalCommandContract(t *testing.T) {
	root := newControlRoot(t)

	t.Run("samples вне диапазона", func(t *testing.T) {
		for _, value := range []string{"0", "21", "-3"} {
			_, code, stderr := runCLI(t, "eval", "--target", root, "--samples", value)
			if code != 1 {
				t.Fatalf("--samples %s: ожидался exit 1, получен %d", value, code)
			}
			if !strings.Contains(stderr, "--samples") {
				t.Fatalf("--samples %s: ожидалась диагностика диапазона, получено: %q", value, stderr)
			}
		}
	})

	t.Run("без цели оценки", func(t *testing.T) {
		_, code, stderr := runCLI(t, "eval", "--target", root)
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "--artifact") || !strings.Contains(stderr, "--agent") {
			t.Fatalf("диагностика должна называть допустимые комбинации, получено: %q", stderr)
		}
	})

	t.Run("недопустимое имя фичи", func(t *testing.T) {
		_, code, stderr := runCLI(t, "eval", "--target", root,
			"--agent", "coder", "--feature", "../escape", "--task", "t")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "недопустимое имя фичи") {
			t.Fatalf("path traversal в --feature обязан отклоняться, получено: %q", stderr)
		}
	})
}

// TestCIImportCommandContract — `ci-import` печатает effective suite ДО
// запуска и не исполняет произвольный YAML: неизвестный формат отклоняется,
// неотображаемые шаги попадают в skip, а не в suite.
func TestCIImportCommandContract(t *testing.T) {
	t.Run("неизвестный формат", func(t *testing.T) {
		_, code, stderr := runCLI(t, "ci-import", "--target", newControlRoot(t), "--format", "gitlab-ci")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "gitlab-ci") {
			t.Fatalf("диагностика должна называть неизвестный формат, получено: %q", stderr)
		}
	})

	t.Run("импорт github-actions", func(t *testing.T) {
		root := newControlRoot(t)
		workflowDir := filepath.Join(root, ".github", "workflows")
		if err := os.MkdirAll(workflowDir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "name: ci\non: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n" +
			"      - uses: actions/checkout@v4\n      - run: go test ./...\n"
		if err := os.WriteFile(filepath.Join(workflowDir, "ci.yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		// --json: весь effective suite приходит на stdout как JSONL, поэтому
		// проверяется машинный контракт, а не форматирование.
		stdout, code, stderr := runCLI(t, "ci-import", "--json", "--target", root)
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		records := parseJSONL(t, stdout)
		var summary, check, skipped *logging.Record
		for index := range records {
			switch records[index].Type {
			case "ci-import":
				summary = &records[index]
			case "ci-import-check":
				check = &records[index]
			case "ci-import-skipped":
				skipped = &records[index]
			}
		}
		if summary == nil {
			t.Fatalf("ожидалась сводка импорта:\n%s", stdout)
		}
		if count, _ := summary.Data["checks"].(float64); count != 1 {
			t.Fatalf("ожидался ровно один импортированный check, получено %v", summary.Data["checks"])
		}
		if fingerprint, _ := summary.Data["fingerprint"].(string); fingerprint == "" {
			t.Fatalf("suite обязан объявлять fingerprint ДО запуска: %v", summary.Data)
		}
		if check == nil || !strings.Contains(check.Message, "go-test") {
			t.Fatalf("`run: go test` обязан импортироваться в suite:\n%s", stdout)
		}
		// actions/checkout вне whitelist: он обязан быть пропущен, а не
		// превращён в исполняемый check.
		if skipped == nil || !strings.Contains(skipped.Message, "skip") {
			t.Fatalf("неотображаемый шаг обязан попадать в skip:\n%s", stdout)
		}
	})
}

// TestSchedulerWorkerAndWebRequireControlRoot — оба long-running режима
// обязаны отказываться на неинициализированном target до открытия БД/порта.
func TestSchedulerWorkerAndWebRequireControlRoot(t *testing.T) {
	for _, command := range []string{"scheduler-worker", "web"} {
		_, code, stderr := runCLI(t, command, "--target", t.TempDir())
		if code != 1 {
			t.Fatalf("%s: ожидался exit 1, получен %d; stderr: %s", command, code, stderr)
		}
		if !strings.Contains(stderr, "не инициализирован") {
			t.Fatalf("%s: ожидался отказ по control root, получено: %q", command, stderr)
		}
	}
}

// TestInitCommandContract — `init` создаёт control root и конфиг; неизвестный
// профиль обязан отклоняться до создания чего-либо необратимого, позиционный
// аргумент — опечатка.
func TestInitCommandContract(t *testing.T) {
	t.Run("позиционный аргумент", func(t *testing.T) {
		_, code, stderr := runCLI(t, "init", "--target", t.TempDir(), "standard")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "Неожиданные аргументы init") {
			t.Fatalf("позиционный аргумент должен отклоняться, получено: %q", stderr)
		}
	})

	t.Run("неизвестный профиль", func(t *testing.T) {
		target := t.TempDir()
		_, code, stderr := runCLI(t, "init", "--target", target, "--profile", "turbo")
		if code != 1 {
			t.Fatalf("ожидался exit 1, получен %d", code)
		}
		if !strings.Contains(stderr, "профил") {
			t.Fatalf("ожидался отказ по профилю, получено: %q", stderr)
		}
		if _, err := os.Stat(filepath.Join(target, ".ai-team", "config.yaml")); err == nil {
			t.Fatal("конфиг не должен создаваться при неизвестном профиле")
		}
	})

	t.Run("standard создаёт конфиг и структуру", func(t *testing.T) {
		target := t.TempDir()
		stdout, code, stderr := runCLI(t, "init", "--target", target, "--write-gitignore")
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "инициализирован") {
			t.Fatalf("ожидалось подтверждение инициализации:\n%s", stdout)
		}
		for _, relative := range []string{
			filepath.Join(".ai-team", "config.yaml"),
			filepath.Join(".ai-team", "artifacts", "tasks"),
			filepath.Join(".ai-team", "reports"),
			filepath.Join(".ai-team", "logs"),
		} {
			if _, err := os.Stat(filepath.Join(target, relative)); err != nil {
				t.Fatalf("init не создал %s: %v", relative, err)
			}
		}
		gitignore, err := os.ReadFile(filepath.Join(target, ".gitignore"))
		if err != nil || !strings.Contains(string(gitignore), ".ai-team/") {
			t.Fatalf("--write-gitignore обязан исключать control root: %q (%v)", gitignore, err)
		}
		// Повторный init идемпотентен и не перезаписывает конфиг.
		before, err := os.ReadFile(filepath.Join(target, ".ai-team", "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if _, code, stderr := runCLI(t, "init", "--target", target, "--write-gitignore"); code != 0 {
			t.Fatalf("повторный init: exit %d; stderr: %s", code, stderr)
		}
		after, err := os.ReadFile(filepath.Join(target, ".ai-team", "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("повторный init перезаписал существующий конфиг")
		}
	})

	t.Run("fast-профиль кладёт project-local reviewer", func(t *testing.T) {
		target := t.TempDir()
		_, code, stderr := runCLI(t, "init", "--target", target, "--profile", "fast")
		if code != 0 {
			t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		def, err := os.ReadFile(filepath.Join(target, ".ai-team", "agents", "reviewer", "def.yaml"))
		if err != nil {
			t.Fatalf("fast-профиль обязан создавать override reviewer: %v", err)
		}
		if !strings.Contains(string(def), "verification") {
			t.Fatalf("override reviewer обязан объявлять verification-выход:\n%s", def)
		}
		prompt, err := os.ReadFile(filepath.Join(target, ".ai-team", "agents", "reviewer", "prompt.md"))
		if err != nil || !strings.Contains(string(prompt), "Верификация") {
			t.Fatalf("prompt override обязан содержать секцию верификации: %v", err)
		}
	})
}

// TestGlobalOutputFlagsAreStrippedBeforeSubcommand — OPS-6: --json/--quiet
// работают и до, и после имени подкоманды и не должны доезжать до FlagSet
// подкоманды (иначе она упадёт на неизвестном флаге).
func TestGlobalOutputFlagsAreStrippedBeforeSubcommand(t *testing.T) {
	root := newControlRoot(t)
	for _, args := range [][]string{
		{"--json", "list", "--target", root},
		{"list", "--json", "--target", root},
		{"--quiet", "list", "--target", root},
		{"list", "-q", "--target", root},
	} {
		stdout, code, stderr := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v: ожидался exit 0, получен %d; stderr: %s", args, code, stderr)
		}
		if strings.Contains(stderr, "flag provided but not defined") {
			t.Fatalf("%v: глобальный флаг доехал до подкоманды: %s", args, stderr)
		}
		// В quiet/json режимах человеческая таблица не должна идти в stdout.
		if strings.Contains(stdout, "Runtime") {
			t.Fatalf("%v: человеческая таблица не должна попадать в stdout: %s", args, stdout)
		}
	}
}
