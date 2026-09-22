package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newListFixture готовит проект с одним валидным агентом в project-слое
// (.ai-team/agents). Имя намеренно не пересекается с built-in набором,
// иначе присутствие агента в выводе не доказывало бы, что прочитан именно
// указанный каталог.
func newListFixture(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".ai-team", "agents", name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	def := "name: " + name + "\n" +
		"description: fixture agent\n" +
		"runtime: agentcli\n" +
		"cli: opencode\n" +
		"prompt_file: prompt.md\n" +
		"mutation: none\n"
	if err := os.WriteFile(filepath.Join(agentDir, "def.yaml"), []byte(def), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "prompt.md"), []byte("fixture prompt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestListHonoursTargetFlag — issue #105: `list` обязан показывать layered
// registry каталога из --target, а не текущей директории процесса.
func TestListHonoursTargetFlag(t *testing.T) {
	const name = "fixture-list-agent"
	dir := newListFixture(t, name)

	stdout, code, stderr := runCLI(t, "list", "--target", dir)
	if code != 0 {
		t.Fatalf("ожидался exit 0, получен %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Fatalf("агент %q из --target %s отсутствует в выводе:\n%s", name, dir, stdout)
	}
	// Источник победившего определения — project-слой указанного каталога.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, name) && !strings.Contains(line, "project") {
			t.Fatalf("ожидался источник project для %q, строка: %q", name, line)
		}
	}
	// Built-in слой обязан остаться в выдаче: --target меняет project-слой,
	// а не заменяет всю цепочку.
	if !strings.Contains(stdout, "coder") {
		t.Fatalf("built-in агенты пропали из layered registry:\n%s", stdout)
	}

	// Без --target тот же fixture виден быть не должен: иначе тест прошёл бы
	// и на старой реализации, игнорировавшей аргументы.
	plainStdout, plainCode, plainStderr := runCLI(t, "list")
	if plainCode != 0 {
		t.Fatalf("ожидался exit 0 для list без аргументов, получен %d; stderr: %s", plainCode, plainStderr)
	}
	if strings.Contains(plainStdout, name) {
		t.Fatalf("list без --target не должен видеть fixture из %s:\n%s", dir, plainStdout)
	}
	if !strings.Contains(plainStdout, "coder") {
		t.Fatalf("list без аргументов должен показывать built-in агентов:\n%s", plainStdout)
	}
}

// TestListRejectsUnknownFlag — issue #105: неизвестный флаг обязан
// завершаться ненулевым кодом с диагностикой, а не молча игнорироваться.
func TestListRejectsUnknownFlag(t *testing.T) {
	_, code, stderr := runCLI(t, "list", "--nonexistent-flag", "xyz")
	if code == 0 {
		t.Fatalf("неизвестный флаг должен давать ненулевой exit; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "nonexistent-flag") {
		t.Fatalf("диагностика должна называть неизвестный флаг, получено: %q", stderr)
	}
}

// TestListRejectsPositionalArgument — позиционный аргумент у `list` смысла
// не имеет и почти всегда означает опечатку; проглатывать его молча нельзя.
func TestListRejectsPositionalArgument(t *testing.T) {
	_, code, stderr := runCLI(t, "list", "coder")
	if code == 0 {
		t.Fatalf("позиционный аргумент должен давать ненулевой exit; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "ai-team list") {
		t.Fatalf("ожидалась подсказка по использованию, получено: %q", stderr)
	}
}

// TestListRejectsMissingTarget — несуществующий `--target` обязан падать, а
// не печатать built-in реестр, выдавая его за реестр указанного проекта:
// это ровно тот режим отказа, против которого заводился issue #105.
// Каталог без `.ai-team` при этом остаётся законным случаем.
func TestListRejectsMissingTarget(t *testing.T) {
	t.Run("nonexistent-dir", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "definitely-not-here")
		stdout, code, stderr := runCLI(t, "list", "--target", missing)
		if code == 0 {
			t.Fatalf("несуществующий --target должен давать ненулевой exit; stdout:\n%s", stdout)
		}
		if !strings.Contains(stderr, missing) {
			t.Fatalf("диагностика должна называть отсутствующий путь, получено: %q", stderr)
		}
		if strings.Contains(stdout, "coder") {
			t.Fatalf("built-in реестр не должен печататься для несуществующего target:\n%s", stdout)
		}
	})

	t.Run("dir-without-control-root", func(t *testing.T) {
		stdout, code, stderr := runCLI(t, "list", "--target", t.TempDir())
		if code != 0 {
			t.Fatalf("каталог без .ai-team законен, ожидался exit 0, получен %d; stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "coder") {
			t.Fatalf("для каталога без .ai-team должен показываться built-in слой:\n%s", stdout)
		}
	})
}

// TestListRejectsTargetWithoutValue — покрытие сценария «Флаги list» в
// openspec/specs/cli-interface/spec.md: отсутствующее значение флага тоже
// обязано давать ненулевой exit.
func TestListRejectsTargetWithoutValue(t *testing.T) {
	_, code, stderr := runCLI(t, "list", "--target")
	if code == 0 {
		t.Fatalf("--target без значения должен давать ненулевой exit; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "target") {
		t.Fatalf("диагностика должна называть флаг target, получено: %q", stderr)
	}
}
