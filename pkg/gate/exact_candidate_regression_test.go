package gate

import (
	"context"
	"testing"
)

// Regression A1 (AUD-01, task-AUD-14): candidate-worktree статус gate обязан
// соответствовать ТОЛЬКО точному candidate-коммиту. Когда кандидат — явный
// commit, идентичность CandidateCommit/CandidateTree берётся из дерева именно
// этого коммита, а НЕ из текущего checkout: перевод рабочей копии на другой
// коммит не должен подменять candidate'а, менять diff или вердикт.
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

	// Рабочая копия переведена на base — она больше НЕ соответствует кандидату.
	gitCmd(t, repo, "checkout", "-q", base)
	mismatched, code, err := Run(context.Background(), Options{
		TargetDir: repo, Base: base, Candidate: candidate, Config: cfg,
	})
	if err != nil || code != ExitPass {
		t.Fatalf("Run (worktree != candidate): code=%d err=%v", code, err)
	}
	if mismatched.CandidateCommit != candidate {
		t.Fatalf("candidate identity подменён рабочим деревом: %q != %q", mismatched.CandidateCommit, candidate)
	}
	if mismatched.CandidateTree != candidateTree {
		t.Fatalf("candidate tree подменён рабочим деревом: %q != %q", mismatched.CandidateTree, candidateTree)
	}
	if mismatched.CandidateTree == workingTreeSHA(context.Background(), repo) {
		t.Fatal("candidate tree не должен быть stat-меткой рабочего дерева")
	}
	if len(mismatched.Mutations) != len(matching.Mutations) {
		t.Fatalf("diff изменился от состояния рабочего дерева: %+v -> %+v", matching.Mutations, mismatched.Mutations)
	}
	if mismatched.PolicyVerdict != matching.PolicyVerdict || mismatched.Status != matching.Status {
		t.Fatalf("вердикт изменился от состояния рабочего дерева: %s/%s -> %s/%s",
			matching.PolicyVerdict, matching.Status, mismatched.PolicyVerdict, mismatched.Status)
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
