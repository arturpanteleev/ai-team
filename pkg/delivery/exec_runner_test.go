package delivery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBin кладёт исполняемый скрипт с именем name и телом body в отдельный
// каталог и возвращает путь к этому каталогу.
func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// QS-06. Зависший `gh` не держит контроллер: дедлайн контекста убивает
// процесс, а StepResult честно называет причину.
func TestExecRunnerKillsHangingGhOnDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("репро использует sh-скрипт")
	}
	bin := fakeBin(t, "gh", "#!/bin/sh\nexec sleep 900\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := ExecRunner{}.Run(ctx, t.TempDir(), "gh", "pr", "create")
	elapsed := time.Since(start)

	if elapsed > 15*time.Second {
		t.Fatalf("зависший gh не был убит по дедлайну: ждали %v", elapsed)
	}
	if result.Status != StepFailed {
		t.Fatalf("status=%q, ожидали %q", result.Status, StepFailed)
	}
	if !strings.Contains(result.Reason, "deadline exceeded") {
		t.Fatalf("reason %q не называет дедлайн", result.Reason)
	}
}

// QS-06. Внешние команды доставки исполняются без интерактива: git не
// спрашивает креды в терминале, askpass-хуки не наследуются. Иначе запрос
// пароля в отсутствующий терминал становится вечным зависанием.
func TestExecRunnerRunsCommandsNonInteractively(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("репро использует sh-скрипт")
	}
	bin := fakeBin(t, "printenv-probe", "#!/bin/sh\nenv\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_ASKPASS", "/usr/bin/true")
	t.Setenv("SSH_ASKPASS", "/usr/bin/true")

	result := ExecRunner{}.Run(context.Background(), t.TempDir(), "printenv-probe")
	if result.Status != StepPassed {
		t.Fatalf("проба окружения не выполнилась: %+v", result)
	}
	env := result.Stdout
	if !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("GIT_TERMINAL_PROMPT=0 не передан ребёнку:\n%s", env)
	}
	if !strings.Contains(env, "GH_PROMPT_DISABLED=1") {
		t.Fatalf("GH_PROMPT_DISABLED=1 не передан ребёнку:\n%s", env)
	}
	if strings.Contains(env, "GIT_ASKPASS=") || strings.Contains(env, "SSH_ASKPASS=") {
		t.Fatalf("askpass-хуки унаследованы — запрос кредов уйдёт в GUI и подвесит доставку:\n%s", env)
	}
}

func TestNonInteractiveEnv(t *testing.T) {
	result := NonInteractiveEnv([]string{
		"PATH=/bin",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_ASKPASS=/opt/gui-askpass",
		"SSH_ASKPASS=/opt/gui-askpass",
	})
	joined := strings.Join(result, "\n")
	for _, want := range []string{"PATH=/bin", "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GIT_SSH_COMMAND=ssh -o BatchMode=yes"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("окружение не содержит %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "GIT_TERMINAL_PROMPT=1") || strings.Contains(joined, "ASKPASS=/opt/gui-askpass") {
		t.Fatalf("интерактивные значения пользователя не перекрыты:\n%s", joined)
	}

	// Явный GIT_SSH_COMMAND пользователя уважается: подменять рабочую
	// конфигурацию доступа мы не вправе.
	custom := NonInteractiveEnv([]string{"GIT_SSH_COMMAND=ssh -i /keys/id"})
	if !strings.Contains(strings.Join(custom, "\n"), "GIT_SSH_COMMAND=ssh -i /keys/id") {
		t.Fatalf("пользовательский GIT_SSH_COMMAND перезаписан: %v", custom)
	}
	if strings.Count(strings.Join(custom, "\n"), "GIT_SSH_COMMAND=") != 1 {
		t.Fatalf("GIT_SSH_COMMAND задан дважды: %v", custom)
	}
}
