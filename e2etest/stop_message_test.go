package e2etest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stopMessage — финальная человеческая строка терминальной остановки run.
// Считаем вхождения именно по ней: она формируется одним местом в cmd/ai-team,
// поэтому >1 вхождение означает, что ту же ошибку печатает ещё кто-то.
const stopMessage = "Пайплайн остановлен:"

// countStopLines считает строки вывода, содержащие финальное сообщение об
// остановке. Сравниваем по строкам, а не по подстрокам: человеческая строка и
// JSON-record несут один и тот же текст, но живут в разных потоках.
func countStopLines(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, stopMessage) {
			count++
		}
	}
	return count
}

// TestE2E_StopMessagePrintedOnce фиксирует issue #112: финальная строка
// `✗ Пайплайн остановлен: …` печаталась дважды, потому что cmd/ai-team писал
// её напрямую в stderr и дополнительно отдавал тот же текст в logging.Emit,
// который в human/quiet режимах печатает Message ещё раз.
//
// Сценарии специально разные по происхождению ошибки (guard до агентов,
// падение runtime, отказ resume по evidence), но все сходятся в один
// терминальный путь вывода — тест доказывает, что путь один и печатает один раз.
func TestE2E_StopMessagePrintedOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Run("dirty git workspace", func(t *testing.T) {
		dir := t.TempDir()
		bin := buildBinary(t)
		pathEnv := setupMock(t)
		setupDeliveryGit(t, dir)

		if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
			t.Fatalf("init failed (%d):\n%s", code, out)
		}
		// Пользовательское изменение в tracked-файле: новый run обязан
		// отказаться ещё до запуска агентов.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("user edit\n"), 0644); err != nil {
			t.Fatal(err)
		}

		code, out := runAI(t, bin, dir, []string{pathEnv},
			"run", "--feature", "stop-dirty", "--task", "dirty workspace", "--approve-gates")
		if code == 0 {
			t.Fatalf("грязный workspace обязан остановить run:\n%s", out)
		}
		if !strings.Contains(out, "clean git workspace") {
			t.Fatalf("ожидалась ошибка про clean git workspace:\n%s", out)
		}
		if got := countStopLines(out); got != 1 {
			t.Fatalf("сообщение об остановке напечатано %d раз(а), ожидался 1:\n%s", got, out)
		}
	})

	t.Run("agent crash", func(t *testing.T) {
		dir := t.TempDir()
		bin := buildBinary(t)
		pathEnv := setupCrashingRuntime(t)

		if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
			t.Fatalf("init failed (%d):\n%s", code, out)
		}

		code, out := runAI(t, bin, dir, []string{pathEnv},
			"run", "--feature", "stop-crash", "--task", "agent crash", "--approve-gates")
		if code == 0 {
			t.Fatalf("падение агента обязано остановить run:\n%s", out)
		}
		if got := countStopLines(out); got != 1 {
			t.Fatalf("сообщение об остановке напечатано %d раз(а), ожидался 1:\n%s", got, out)
		}
	})

	t.Run("broken evidence chain on resume", func(t *testing.T) {
		dir := t.TempDir()
		bin := buildBinary(t)
		pathEnv := setupMock(t)
		setupDeliveryGit(t, dir)

		if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
			t.Fatalf("init failed (%d):\n%s", code, out)
		}

		// Первый run доходит до точного delivery-approval и оставляет
		// resumable state с evidence-цепочкой (exit 3).
		code, out := runAI(t, bin, dir, []string{pathEnv},
			"run", "--feature", "stop-evidence", "--task", "evidence chain", "--approve-gates")
		if code != 3 {
			t.Fatalf("ожидалась остановка на delivery approval (exit 3), got %d:\n%s", code, out)
		}
		runID, planHash := extractResumeHints(t, out)
		corruptEventChain(t, filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"))

		// --approve-plan снимает pending approval, поэтому resume доходит до
		// fail-closed верификации evidence — именно её отказ описан в issue.
		code, out = runAI(t, bin, dir, []string{pathEnv},
			"run", "--resume", runID, "--approve-gates", "--approve-plan", planHash)
		if code == 0 {
			t.Fatalf("сломанная evidence-цепочка обязана остановить resume:\n%s", out)
		}
		if !strings.Contains(out, "event chain") {
			t.Fatalf("ожидался отказ по event chain:\n%s", out)
		}
		if got := countStopLines(out); got != 1 {
			t.Fatalf("сообщение об остановке напечатано %d раз(а), ожидался 1:\n%s", got, out)
		}
	})

	// --quiet: человеческая строка остаётся (ошибка критична), но по-прежнему
	// одна. До фикса quiet печатал её дважды, как и обычный режим.
	t.Run("quiet mode", func(t *testing.T) {
		dir := t.TempDir()
		bin := buildBinary(t)
		pathEnv := setupCrashingRuntime(t)

		if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
			t.Fatalf("init failed (%d):\n%s", code, out)
		}

		code, out := runAI(t, bin, dir, []string{pathEnv},
			"run", "--feature", "stop-quiet", "--task", "quiet stop", "--approve-gates", "--quiet")
		if code == 0 {
			t.Fatalf("падение агента обязано остановить run:\n%s", out)
		}
		if got := countStopLines(out); got != 1 {
			t.Fatalf("сообщение об остановке напечатано %d раз(а), ожидался 1:\n%s", got, out)
		}
	})

	// --json: stdout несёт machine-readable record, stderr — ровно одну
	// человеческую строку. Разные потоки дублированием не являются.
	t.Run("json mode", func(t *testing.T) {
		dir := t.TempDir()
		bin := buildBinary(t)
		pathEnv := setupCrashingRuntime(t)

		if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
			t.Fatalf("init failed (%d):\n%s", code, out)
		}

		stdout, stderr, code := runAIJSON(t, bin, dir, []string{pathEnv},
			"run", "--feature", "stop-json", "--task", "json stop", "--approve-gates", "--json")
		if code == 0 {
			t.Fatalf("падение агента обязано остановить run:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if got := countStopLines(stderr); got != 1 {
			t.Fatalf("stderr: сообщение об остановке напечатано %d раз(а), ожидался 1:\n%s", got, stderr)
		}
		if got := countStopLines(stdout); got != 1 {
			t.Fatalf("stdout: ожидался ровно один JSON-record об остановке, got %d:\n%s", got, stdout)
		}
	})
}

// setupCrashingRuntime подкладывает в PATH runtime, который проходит
// preflight (--version), но падает на любом вызове агента — сценарий
// «упал агент».
func setupCrashingRuntime(t *testing.T) string {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "crashbin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir crashbin: %v", err)
	}
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "opencode mock 1.0.0"
  exit 0
fi
echo "MOCK: runtime crashed" >&2
exit 7
`
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0755); err != nil {
		t.Fatalf("write crashing opencode: %v", err)
	}
	return "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// extractResumeHints достаёт run_id и SHA-256 canonical delivery plan из
// человеческой подсказки о resume.
func extractResumeHints(t *testing.T, output string) (runID, planHash string) {
	t.Helper()
	match := regexp.MustCompile(`--resume ([^ ]+) --approve-plan ([a-f0-9]{64})`).FindStringSubmatch(output)
	if len(match) != 3 {
		t.Fatalf("resume-подсказка не найдена в выводе:\n%s", output)
	}
	return match[1], match[2]
}

// corruptEventChain ломает hash-цепочку events.jsonl, меняя данные второго
// события: resume обязан отказать на верификации evidence.
func corruptEventChain(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("в events.jsonl меньше двух событий:\n%s", raw)
	}
	lines[1] = strings.Replace(lines[1], `"type":"`, `"type":"x`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
}
