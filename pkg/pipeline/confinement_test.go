package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAgentWriteOutsideWorktreeIsNeitherBlockedNorRecorded фиксирует вторую
// половину QS-04: агент выполнил `echo … > ../coder-escaped-worktree.txt` и
// создал файл за пределами worktree без единого замечания.
//
// Тест намеренно выключен и НЕ описывает текущее поведение — он описывает
// требуемое. Оставлен как исполняемая формулировка дефекта: снимите t.Skip,
// когда появится confinement, и тест станет его приёмкой.
//
// Почему не починено здесь. Mutation guard сравнивает снимки одного каталога
// — рабочего дерева (captureWorkspaceSnapshot(rs.sourceDir())). Запись мимо
// него невидима не потому, что забыли посмотреть, а потому, что смотреть
// некуда: агент — обычный дочерний процесс с правами контроллера и может
// писать в $HOME, /tmp, соседний репозиторий. Снять снимок «всего остального»
// нельзя, а снимок одного родительского каталога поймал бы ровно тот пример
// из issue и пропустил всё прочее — это хуже, чем ничего: такая проверка
// выглядит защитой, не будучи ею. Настоящее решение — запуск агента в
// песочнице (отдельный mount/PID namespace, seccomp или контейнер) с
// доступом только к worktree; это отдельная задача, а не правка на месте.
func TestAgentWriteOutsideWorktreeIsNeitherBlockedNorRecorded(t *testing.T) {
	t.Skip("QS-04, вторая половина: confinement агента не реализован; нужна песочница, а не правка mutation guard")

	root := t.TempDir()
	worktree := filepath.Join(root, "repo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := captureWorkspaceSnapshot(worktree)
	if err != nil {
		t.Fatal(err)
	}

	// Ровно то, что сделал агент в воспроизведении из issue.
	escaped := filepath.Join(root, "coder-escaped-worktree.txt")
	if err := os.WriteFile(escaped, []byte("escaped\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := captureWorkspaceSnapshot(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if changed := changedSnapshotPaths(before, after); len(changed) == 0 {
		t.Fatal("запись за пределами worktree не отражена ни в одной мутации")
	}
}
