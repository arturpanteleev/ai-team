package retention

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	oldTerminalRunID = "run-old-terminal"
	newTerminalRunID = "run-new-terminal"
	activeRunID      = "run-active"
	orphanRunID      = "run-orphan"
)

type fixture struct {
	target string
}

func writeState(t *testing.T, target, runID, phase string, updatedAt time.Time) {
	t.Helper()
	path := filepath.Join(target, ".ai-team", "state", "runs", runID+".json")
	data := `{"schema_version":1,"run_id":"` + runID + `","phase":"` + phase +
		`","updated_at":"` + updatedAt.UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write state %s: %v", runID, err)
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return content
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{target: t.TempDir()}
	base := filepath.Join(f.target, ".ai-team")
	now := time.Now().UTC()
	old := now.Add(-1000 * time.Hour)

	for _, dir := range [][]string{
		{"state", "runs"}, {"state", "candidates"}, {"state", "approvals"}, {"state", "exports"},
		{"worktrees"}, {"runs"},
	} {
		if err := os.MkdirAll(filepath.Join(append([]string{base}, dir...)...), 0755); err != nil {
			t.Fatal(err)
		}
	}

	writeState(t, f.target, oldTerminalRunID, "terminal", old)
	writeState(t, f.target, newTerminalRunID, "terminal", now)
	writeState(t, f.target, activeRunID, "running", old)

	// Worktrees: terminal-раны и сирота удаляются; активный остаётся.
	for _, item := range []struct {
		runID   string
		content string
	}{
		{oldTerminalRunID, "wt-old-payload"},
		{newTerminalRunID, "wt-new-payload"},
		{activeRunID, "wt-active-payload"},
		{orphanRunID, "wt-orphan-payload"},
	} {
		writeFile(t, filepath.Join(base, "worktrees", item.runID, "data.txt"), item.content)
	}

	// Immutable runs evidence.
	writeFile(t, filepath.Join(base, "runs", oldTerminalRunID, "manifest.json"), `{"run_id":"`+oldTerminalRunID+`"}`)
	writeFile(t, filepath.Join(base, "runs", newTerminalRunID, "manifest.json"), `{"run_id":"`+newTerminalRunID+`"}`)
	writeFile(t, filepath.Join(base, "runs", activeRunID, "manifest.json"), `{"run_id":"`+activeRunID+`"}`)

	// Mutable state.
	writeFile(t, filepath.Join(base, "state", "candidates", oldTerminalRunID+".json"), `{"run_id":"`+oldTerminalRunID+`"}`)
	writeFile(t, filepath.Join(base, "state", "approvals", oldTerminalRunID, "appr.json"), `{"id":"appr"}`)

	// Verified export Guard (V0-0): только старый terminal run экспортирован.
	writeExport(t, f.target, oldTerminalRunID)
	return f
}

func writeExport(t *testing.T, target, runID string) {
	t.Helper()
	path := filepath.Join(target, ".ai-team", "state", "exports", runID+".json")
	data := fmt.Sprintf(`{"schema_version":1,"run_id":%q,"verified":true,"bundle_sha256":"face01","exported_at":"2026-08-01T00:00:00Z"}`, runID)
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write export %s: %v", runID, err)
	}
}

func (f *fixture) build(t *testing.T, pruneRuns bool) *Plan {
	t.Helper()
	plan, err := Build(Options{
		Target: f.target, OlderThan: 720 * time.Hour, KeepLast: 1, PruneRuns: pruneRuns,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return plan
}

func actionsFor(plan *Plan, category string) []Action {
	var result []Action
	for _, action := range plan.Actions {
		if action.Category == category {
			result = append(result, action)
		}
	}
	return result
}

func TestDryRunCountsAndDeletesNothing(t *testing.T) {
	f := newFixture(t)
	plan := f.build(t, true)

	worktrees := actionsFor(plan, CategoryWorktrees)
	if len(worktrees) != 3 {
		t.Fatalf("ожидались 3 worktree-действия (2 terminal + сирота), получено %d", len(worktrees))
	}
	// keep-last=1 защищает новый terminal run от уборки по возрасту,
	// поэтому по возрасту eligible только старый terminal run.
	stateActions := actionsFor(plan, CategoryState)
	if len(stateActions) != 3 {
		t.Fatalf("ожидались 3 state-действия (lifecycle+candidates+approvals), получено %d", len(stateActions))
	}
	runActions := actionsFor(plan, CategoryRuns)
	if len(runActions) != 1 || filepath.Base(runActions[0].Path) != oldTerminalRunID {
		t.Fatalf("ожидалось удаление только старого terminal run evidence, получено %+v", runActions)
	}
	if got := totalBytesOf(t, plan); got != plan.TotalBytes() {
		t.Fatalf("TotalBytes %d не совпадает с фактическим размером объектов %d", plan.TotalBytes(), got)
	}

	// Dry-run: план построен, но диск не тронут.
	for _, action := range plan.Actions {
		if !exists(action.Path) {
			t.Fatalf("dry-run удалил %s", action.Path)
		}
	}
}

func TestExecuteRemovesOrphanAndTerminalWorktreesKeepsActive(t *testing.T) {
	f := newFixture(t)
	plan := f.build(t, false)
	if err := plan.Execute(f.target); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	base := filepath.Join(f.target, ".ai-team")
	if exists(filepath.Join(base, "worktrees", orphanRunID)) {
		t.Fatal("сирота-worktree должен быть удалён без lifecycle state")
	}
	if exists(filepath.Join(base, "worktrees", newTerminalRunID)) {
		t.Fatal("terminal worktree должен быть удалён даже без --prune-runs")
	}
	if !exists(filepath.Join(base, "worktrees", activeRunID)) {
		t.Fatal("активный (non-terminal) worktree нельзя трогать")
	}
}

func TestRunsEvidenceOnlyWithPruneRunsFlag(t *testing.T) {
	f := newFixture(t)
	plan := f.build(t, false)
	if err := plan.Execute(f.target); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	base := filepath.Join(f.target, ".ai-team")
	if !exists(filepath.Join(base, "runs", oldTerminalRunID)) {
		t.Fatal("immutable runs нельзя удалять без --prune-runs")
	}

	f = newFixture(t)
	base = filepath.Join(f.target, ".ai-team")
	plan = f.build(t, true)
	if err := plan.Execute(f.target); err != nil {
		t.Fatalf("Execute with --prune-runs: %v", err)
	}
	if exists(filepath.Join(base, "runs", oldTerminalRunID)) {
		t.Fatal("старый terminal run должен быть удалён с --prune-runs")
	}
	if !exists(filepath.Join(base, "runs", newTerminalRunID)) {
		t.Fatal("keep-last защищает свежий terminal run от удаления")
	}
	if !exists(filepath.Join(base, "runs", activeRunID)) {
		t.Fatal("активный run evidence нельзя удалять никогда")
	}
}

func TestMutableStateCleanedWithoutPruneRuns(t *testing.T) {
	f := newFixture(t)
	plan := f.build(t, false)
	if err := plan.Execute(f.target); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	base := filepath.Join(f.target, ".ai-team")
	if exists(filepath.Join(base, "state", "runs", oldTerminalRunID+".json")) {
		t.Fatal("lifecycle state старого terminal run должен быть убран")
	}
	if exists(filepath.Join(base, "state", "candidates", oldTerminalRunID+".json")) {
		t.Fatal("candidate metadata старого terminal run должна быть убрана")
	}
	if exists(filepath.Join(base, "state", "approvals", oldTerminalRunID)) {
		t.Fatal("approvals старого terminal run должны быть убраны")
	}
	if !exists(filepath.Join(base, "state", "runs", activeRunID+".json")) {
		t.Fatal("non-terminal state нельзя трогать")
	}
	if !exists(filepath.Join(base, "state", "runs", newTerminalRunID+".json")) {
		t.Fatal("keep-last защищает свежий terminal run state")
	}
}

func TestRefusesPathOutsideControlRoot(t *testing.T) {
	plan := &Plan{Actions: []Action{{Category: CategoryState, Path: "/etc/passwd"}}}
	if err := plan.Execute(t.TempDir()); err == nil {
		t.Fatal("удаление вне .ai-team должно быть отклонено")
	}
}

func totalBytesOf(t *testing.T, plan *Plan) int64 {
	t.Helper()
	var total int64
	for _, action := range plan.Actions {
		info, err := os.Stat(action.Path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			total += info.Size()
			continue
		}
		size, err := directorySize(action.Path)
		if err != nil {
			t.Fatalf("directorySize(%s): %v", action.Path, err)
		}
		total += size
	}
	return total
}

func TestRunEvidencePrunedOnlyWithVerifiedExport(t *testing.T) {
	f := newFixture(t)
	// Удаляем verified-запись: у старого run нет защищённого portable export.
	if err := os.Remove(filepath.Join(f.target, ".ai-team", "state", "exports", oldTerminalRunID+".json")); err != nil {
		t.Fatal(err)
	}
	plan := f.build(t, true)
	if actions := actionsFor(plan, CategoryRuns); len(actions) != 0 {
		t.Fatalf("V0-0: без verified-export evidence не должна удаляться, получено %+v", actions)
	}
	var skippedOld bool
	for _, skipped := range plan.Skipped {
		if strings.HasSuffix(skipped.Path, oldTerminalRunID) && strings.Contains(skipped.Reason, "state/exports") {
			skippedOld = true
		}
	}
	if !skippedOld {
		t.Fatalf("не-экспортированный run должен попасть в Skipped с причиной state/exports, got %+v", plan.Skipped)
	}
	if err := plan.Execute(f.target); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(f.target, ".ai-team", "runs", oldTerminalRunID)) {
		t.Fatal("выполнение плана не должно удалить evidence без verified-export")
	}
}

func TestRunEvidenceSkippedOnUnverifiedOrMalformedExport(t *testing.T) {
	f := newFixture(t)
	base := filepath.Join(f.target, ".ai-team", "state", "exports", oldTerminalRunID+".json")
	if err := os.WriteFile(base, []byte(`{"schema_version":1,"run_id":"`+oldTerminalRunID+`","verified":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	plan := f.build(t, true)
	if actions := actionsFor(plan, CategoryRuns); len(actions) != 0 {
		t.Fatalf("unverified export не даёт права на удаление, получено %+v", actions)
	}

	f = newFixture(t)
	base = filepath.Join(f.target, ".ai-team", "state", "exports", oldTerminalRunID+".json")
	if err := os.WriteFile(base, []byte(`не json`), 0644); err != nil {
		t.Fatal(err)
	}
	plan = f.build(t, true)
	if actions := actionsFor(plan, CategoryRuns); len(actions) != 0 {
		t.Fatalf("malformed export не даёт права на удаление, получено %+v", actions)
	}

	f = newFixture(t)
	base = filepath.Join(f.target, ".ai-team", "state", "exports", oldTerminalRunID+".json")
	// verified=true с пустым bundle_sha256 — запись без проверяемого bundle:
	// fail-closed, право на удаление не выдаётся.
	if err := os.WriteFile(base, []byte(`{"schema_version":1,"run_id":"`+oldTerminalRunID+`","verified":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	plan = f.build(t, true)
	if actions := actionsFor(plan, CategoryRuns); len(actions) != 0 {
		t.Fatalf("verified export с пустым bundle_sha256 не даёт права на удаление, получено %+v", actions)
	}
	for _, skipped := range plan.Skipped {
		if strings.HasSuffix(skipped.Path, oldTerminalRunID+".json") && strings.Contains(skipped.Reason, "bundle_sha256") {
			return
		}
	}
	t.Fatalf("ожидали Skipped с причиной bundle_sha256, got %+v", plan.Skipped)
}
