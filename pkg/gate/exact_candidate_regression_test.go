package gate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Regression A1 (AUD-01, task-AUD-14): candidate-worktree статус gate обязан
// соответствовать ТОЛЬКО точному candidate-коммиту. Когда кандидат — явный
// commit, идентичность CandidateCommit/CandidateTree берётся из дерева именно
// этого коммита, а НЕ из текущего checkout. После слияния fail-closed guards
// (#95, F-4) перевод рабочей копии на другой коммит больше не даёт PASS —
// gate обязан BLOКировать запуск с явным сообщением о несовпадении, а не
// подменять кандидата, менять diff или вердикт (F-3).
func TestCandidateCommitIdentityIndependentOfWorktree(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"src/app.go":        "package app\n",
		"tests/app_test.go": "package app\n",
	})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	candidate := commitChange(t, repo, "source + tests", map[string]string{
		"src/app.go":        "package app\n// v2\n",
		"tests/app_test.go": "package app\n\nfunc TestApp() {}\n",
	})
	candidateTree := gitCmd(t, repo, "rev-parse", candidate+"^{tree}")
	cfg := &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired}}

	matching, code, err := Run(context.Background(), Options{
		TargetDir: repo, Base: base, Candidate: candidate, Config: cfg,
	})
	if err != nil || code != ExitPass {
		t.Fatalf("Run (worktree == candidate): code=%d err=%v", code, err)
	}
	if matching.CandidateCommit != candidate || matching.CandidateTree != candidateTree {
		t.Fatalf("candidate identity: commit=%q (want %q) tree=%q (want %q)",
			matching.CandidateCommit, candidate, matching.CandidateTree, candidateTree)
	}

	// Рабочая копия переведена на base — содержимое больше НЕ равно кандидату.
	// Fail-closed guard (workingTreeMatchesCommit) обязан заблокировать запуск
	// и назвать candidate-коммит, а не подменить identity рабочим деревом.
	gitCmd(t, repo, "checkout", "-q", base)
	blocked, code, err := Run(context.Background(), Options{
		TargetDir: repo, Base: base, Candidate: candidate, Config: cfg,
	})
	if blocked != nil {
		t.Fatalf("Run (worktree != candidate) должен вернуть nil-результат (BLOCKED), got %+v", blocked)
	}
	if code != ExitBlocked {
		t.Fatalf("Run (worktree != candidate): code=%d, want ExitBlocked=%d", code, ExitBlocked)
	}
	if err == nil {
		t.Fatal("Run (worktree != candidate): блокировка без ошибки")
	}
	var blockedErr *BlockedError
	if !errors.As(err, &blockedErr) {
		t.Fatalf("Run (worktree != candidate): ожидался BlockedError, got %T: %v", err, err)
	}
	if !strings.Contains(blockedErr.Reason, candidate) {
		t.Fatalf("блокирующее сообщение не ссылается на candidate-коммит: %q", blockedErr.Reason)
	}
}

// WORKTREE-кандидат — единственный, кого описывает stat-метка рабочего дерева:
// CandidateCommit пуст, CandidateTree — sha256(`git status --porcelain`) и
// отслеживает НАБОР отклонений рабочего дерева от index (а не содержимое —
// это задокументированная граница метки). Отдельная ветвь identity, не
// путается с commit-кандидатом.
func TestWorktreeCandidateLabelTracksLiveState(t *testing.T) {
	repo := newRepo(t, map[string]string{"src/app.go": "package app\n"})
	base := gitCmd(t, repo, "rev-parse", "HEAD")
	cfg := &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyOff}}
	writeFiles(t, repo, map[string]string{"src/app.go": "package app\n// wip\n"})
	first, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: "WORKTREE", Config: cfg})
	if err != nil || code != ExitPass {
		t.Fatalf("Run 1: code=%d err=%v", code, err)
	}
	if first.CandidateCommit != "" || first.CandidateTree == "" {
		t.Fatalf("WORKTREE: commit=%q tree=%q", first.CandidateCommit, first.CandidateTree)
	}
	// Новый untracked файл меняет набор `git status --porcelain` → метка обязана
	// смениться; commit-кандидат так себя не ведёт (см. тест выше).
	writeFiles(t, repo, map[string]string{"src/extra.go": "package app\n// extra\n"})
	second, code, err := Run(context.Background(), Options{TargetDir: repo, Base: base, Candidate: "WORKTREE", Config: cfg})
	if err != nil || code != ExitPass {
		t.Fatalf("Run 2: code=%d err=%v", code, err)
	}
	if second.CandidateCommit != "" || second.CandidateTree == first.CandidateTree {
		t.Fatalf("WORKTREE-метка не отражает живое состояние дерева: %s", second.CandidateTree)
	}
}
