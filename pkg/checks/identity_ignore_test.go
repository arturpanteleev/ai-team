package checks

import (
	"os"
	"path/filepath"
	"testing"
)

// identity_ignore_test.go — QS-03 (#154): набор «что можно не обходить ради
// скорости» и набор «что исключается из identity» разделены. Из identity
// исключено только controller-owned.

func TestControllerOwnedDirsIsControllerMetadataOnly(t *testing.T) {
	owned := ControllerOwnedDirs()
	if !owned[".git"] || !owned[".ai-team"] {
		t.Fatalf("controller-owned набор обязан содержать .git и .ai-team: %v", owned)
	}
	if len(owned) != 2 {
		t.Fatalf("в controller-owned наборе не должно быть ничего, кроме .git и .ai-team: %v", owned)
	}
	for _, name := range []string{"node_modules", "vendor", "dist", ".venv", "__pycache__"} {
		if owned[name] {
			t.Errorf("%s — часть проекта, он не может быть controller-owned", name)
		}
	}
}

// Конфигурация не имеет права ослабить набор атрибуции: SetExtraIgnoreDirs
// влияет только на workspace digest.
func TestExtraIgnoreDirsDoNotReachControllerOwnedSet(t *testing.T) {
	ResetExtraIgnoreDirs()
	t.Cleanup(ResetExtraIgnoreDirs)
	SetExtraIgnoreDirs([]string{"coverage"})
	if ControllerOwnedDirs()["coverage"] {
		t.Fatal("project-specific ignore просочился в набор атрибуции мутаций")
	}
	if !DefaultIgnoreDirs()["coverage"] {
		t.Fatal("project-specific ignore обязан влиять на workspace digest")
	}
}

// Основной регресс QS-03: запись в каталог зависимостей/сборки обязана менять
// канонический workspace digest.
func TestWorkspaceDigestCoversDependencyAndBuildDirs(t *testing.T) {
	ResetExtraIgnoreDirs()
	t.Cleanup(ResetExtraIgnoreDirs)
	for _, dirName := range []string{"node_modules", "vendor", "dist", ".venv", "__pycache__"} {
		t.Run(dirName, func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, dirName, "pkg")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(nested, "file.txt")
			if err := os.WriteFile(target, []byte("good"), 0o644); err != nil {
				t.Fatal(err)
			}
			before, err := WorkspaceDigest(root)
			if err != nil {
				t.Fatal(err)
			}
			// "bad" той же длины: подмена не ловится по размеру.
			if err := os.WriteFile(target, []byte("badd"), 0o644); err != nil {
				t.Fatal(err)
			}
			after, err := WorkspaceDigest(root)
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("изменение %s/pkg/file.txt не изменило workspace digest", dirName)
			}
		})
	}
}

// Каталог, который проект явно объявил в tree_hash.ignore_dirs, из digest
// по-прежнему исключается — это осознанное решение проекта, а не умолчание.
func TestConfiguredIgnoreDirStillExcludedFromDigest(t *testing.T) {
	ResetExtraIgnoreDirs()
	t.Cleanup(ResetExtraIgnoreDirs)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "app.js"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetExtraIgnoreDirs([]string{"dist"})
	before, err := WorkspaceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "app.js"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := WorkspaceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("явно объявленный ignore-каталог обязан оставаться вне digest")
	}
}

func TestControlMetadataFileDigests(t *testing.T) {
	root := t.TempDir()
	control := filepath.Join(root, ".ai-team")
	for _, dir := range []string{"runs/r1", "artifacts/f1", "worktrees/w1", "state", "agents/reviewer"} {
		if err := os.MkdirAll(filepath.Join(control, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(relative, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(control, relative), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.yaml", "version: 1")
	write("agents/reviewer/def.yaml", "mutation: none")
	write("runs/r1/events.jsonl", "{}")
	write("artifacts/f1/review.md", "ok")
	write("worktrees/w1/x", "x")

	files, before, err := ControlMetadataFileDigests(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["config.yaml"]; !ok {
		t.Fatalf("config.yaml обязан попадать в control metadata snapshot: %v", files)
	}
	if _, ok := files["agents/reviewer/def.yaml"]; !ok {
		t.Fatalf("локальные def'ы агентов обязаны попадать в snapshot: %v", files)
	}
	for _, controllerOwned := range []string{"runs/r1/events.jsonl", "artifacts/f1/review.md", "worktrees/w1/x"} {
		if _, ok := files[controllerOwned]; ok {
			t.Errorf("%s пишет контроллер, его не должно быть в snapshot", controllerOwned)
		}
	}

	// Запись контроллера в собственный подкаталог не двигает fingerprint,
	// запись агента «мимо» artifact namespace — двигает.
	write("runs/r1/events.jsonl", `{"more":true}`)
	if _, unchanged, err := ControlMetadataFileDigests(root); err != nil || unchanged != before {
		t.Fatalf("evidence-запись контроллера не должна менять fingerprint: err=%v", err)
	}
	write("reviewer-was-here.txt", "pwned")
	_, after, err := ControlMetadataFileDigests(root)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("файл, положенный агентом в .ai-team, обязан менять fingerprint")
	}
}

func TestControlMetadataFileDigestsWithoutControlDir(t *testing.T) {
	files, fingerprint, err := ControlMetadataFileDigests(t.TempDir())
	if err != nil {
		t.Fatalf("отсутствие .ai-team — не ошибка: %v", err)
	}
	if len(files) != 0 || fingerprint != "" {
		t.Fatalf("ожидался пустой снапшот, получено files=%v fingerprint=%q", files, fingerprint)
	}
}
