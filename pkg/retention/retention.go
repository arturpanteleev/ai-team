// Package retention строит план уборки растущих артефактов .ai-team:
// candidate worktrees, mutable control state и (только по явному флагу и
// только для verified-экспортированных evidence) immutable run records.
// V0-0 guard: run evidence никогда не удаляется, пока в state/exports нет
// verified-записи о её portable export (V0-4).
package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	_ "modernc.org/sqlite"
)

const (
	CategoryWorktrees = "worktrees"
	CategoryRuns      = "runs"
	CategoryState     = "state"
	CategoryApprovals = "approvals-db"
)

// Options описывает параметры одного прохода gc.
type Options struct {
	Target         string
	OlderThan      time.Duration
	KeepLast       int
	PruneRuns      bool
	ApprovalDBPath string
}

// Action — один объект, подлежащий удалению.
type Action struct {
	Category     string
	Path         string
	RunID        string
	Bytes        int64 // physical bytes expected to be reclaimed from the filesystem
	LogicalBytes int64 // bytes of logical records removed; SQL deletes may not shrink the DB file
}

// Skipped — объект, который gc не тронул и почему.
type Skipped struct {
	Path   string
	Reason string
}

// Plan — полный план уборки. План строится целиком до любых мутаций,
// поэтому --dry-run может его напечатать без побочных эффектов.
type Plan struct {
	Actions []Action
	Skipped []Skipped
}

func (p *Plan) TotalBytes() int64 {
	var total int64
	for _, action := range p.Actions {
		total += action.Bytes
	}
	return total
}

func (p *Plan) TotalLogicalBytes() int64 {
	var total int64
	for _, action := range p.Actions {
		total += action.LogicalBytes
	}
	return total
}

type terminalRun struct {
	runID     string
	updatedAt time.Time
}

type planner struct {
	options    Options
	aiTeam     string
	now        time.Time
	plan       Plan
	terminal   map[string]time.Time
	exports    map[string]bool
	keepSet    map[string]bool
	cutoff     time.Duration
	approvalDB string
}

// Build обходит control-каталог target и возвращает план удаления.
// Никаких изменений на диске Build не делает; удаление выполняет Execute.
func Build(options Options) (*Plan, error) {
	if !filepath.IsAbs(options.Target) {
		return nil, fmt.Errorf("retention: target должен быть абсолютным путём")
	}
	if _, err := safeio.ExistingDir(options.Target, ".ai-team"); err != nil {
		return nil, fmt.Errorf("retention: %w", err)
	}
	aiTeam := filepath.Join(options.Target, ".ai-team")
	approvalDB := options.ApprovalDBPath
	if approvalDB == "" {
		approvalDB = filepath.Join(aiTeam, "web.db")
	} else if !filepath.IsAbs(approvalDB) {
		approvalDB = filepath.Join(options.Target, approvalDB)
	}
	p := &planner{
		options:  options,
		aiTeam:   aiTeam,
		now:      time.Now().UTC(),
		terminal: map[string]time.Time{},
		exports:  map[string]bool{},
		keepSet:  map[string]bool{},
		cutoff:   options.OlderThan, approvalDB: filepath.Clean(approvalDB),
	}
	if err := p.loadTerminalRuns(); err != nil {
		return nil, err
	}
	if err := p.loadVerifiedExports(); err != nil {
		return nil, err
	}
	p.markKeepLast()
	if err := p.planWorktrees(); err != nil {
		return nil, err
	}
	if err := p.planState(); err != nil {
		return nil, err
	}
	if err := p.planDatabaseApprovals(); err != nil {
		return nil, err
	}
	if options.PruneRuns {
		if err := p.planRuns(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(p.plan.Actions, func(i, j int) bool {
		order := map[string]int{CategoryWorktrees: 0, CategoryRuns: 1, CategoryState: 2, CategoryApprovals: 3}
		if order[p.plan.Actions[i].Category] != order[p.plan.Actions[j].Category] {
			return order[p.plan.Actions[i].Category] < order[p.plan.Actions[j].Category]
		}
		return p.plan.Actions[i].Path < p.plan.Actions[j].Path
	})
	return &p.plan, nil
}

// Execute удаляет все объекты плана, проверяя перед каждым удалением, что
// путь всё ещё regular dir/file внутри .ai-team и не является symlink.
func (p *Plan) Execute(target string) error {
	aiTeamRoot := filepath.Join(target, ".ai-team")
	for _, action := range p.Actions {
		if err := insideRoot(aiTeamRoot, action.Path); err != nil {
			return fmt.Errorf("retention: %w", err)
		}
		if action.Category == CategoryApprovals {
			if err := validateApprovalDBPath(target, action.Path); err != nil {
				return fmt.Errorf("retention: unsafe approval DB path: %w", err)
			}
		}
		info, err := os.Lstat(action.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("retention: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("retention: отказ удалять symlink %s", action.Path)
		}
		if action.Category == CategoryWorktrees {
			if err := removeGitWorktree(target, action.Path); err != nil {
				return fmt.Errorf("retention: %w", err)
			}
			continue
		}
		if action.Category == CategoryApprovals {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("retention: approval DB %s must be a regular file", action.Path)
			}
			if err := deleteRunApprovals(action.Path, action.RunID); err != nil {
				return fmt.Errorf("retention: удалить approvals для run %s: %w", action.RunID, err)
			}
			continue
		}
		if err := os.RemoveAll(action.Path); err != nil {
			return fmt.Errorf("retention: удаление %s: %w", action.Path, err)
		}
	}
	return pruneGitWorktrees(target)
}

// loadTerminalRuns читает все lifecycle state файлы и запоминает terminal.
func (p *planner) loadTerminalRuns() error {
	root := filepath.Join(p.aiTeam, "state", "runs")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: filepath.Join(root, name), Reason: "не regular lifecycle state file",
			})
			continue
		}
		runID := strings.TrimSuffix(name, ".json")
		state, err := readLifecycle(filepath.Join(root, name))
		if err != nil {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: filepath.Join(root, name), Reason: fmt.Sprintf("нечитаемый state: %v", err),
			})
			continue
		}
		if state.phase == "terminal" && state.runID == runID {
			p.terminal[runID] = state.updatedAt
		}
	}
	return nil
}

// markKeepLast защищает keep-last самых свежих terminal-ранов от любой уборки
// по возрасту (state, approvals, runs evidence).
func (p *planner) markKeepLast() {
	ids := make([]string, 0, len(p.terminal))
	for runID := range p.terminal {
		ids = append(ids, runID)
	}
	sort.Slice(ids, func(i, j int) bool { return p.terminal[ids[i]].After(p.terminal[ids[j]]) })
	for index, runID := range ids {
		if index >= p.options.KeepLast {
			break
		}
		p.keepSet[runID] = true
	}
}

func (p *planner) eligibleForAge(runID string) bool {
	if p.keepSet[runID] {
		return false
	}
	updated := p.terminal[runID]
	if updated.IsZero() {
		return false
	}
	return p.now.Sub(updated) >= p.cutoff
}

// planWorktrees: worktree удаляется, если его run terminal либо это сирота
// без lifecycle state. Non-terminal (активные) не трогаются.
func (p *planner) planWorktrees() error {
	root := filepath.Join(p.aiTeam, "worktrees")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "worktree должен быть каталогом без symlink",
			})
			continue
		}
		if _, ok := p.lifecyclePhase(entry.Name()); ok && !p.isTerminal(entry.Name()) {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "run ещё non-terminal",
			})
			continue
		}
		size, sizeErr := directorySize(path)
		if sizeErr != nil {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: fmt.Sprintf("не удалось измерить: %v", sizeErr),
			})
			continue
		}
		p.plan.Actions = append(p.plan.Actions, Action{
			Category: CategoryWorktrees, Path: path, Bytes: size,
		})
	}
	return nil
}

// planState: mutable контрольные файлы (lifecycle state, candidate metadata,
// approvals) для terminal-ранов старше older-than вне keep-last.
func (p *planner) planState() error {
	for runID := range p.terminal {
		if !p.eligibleForAge(runID) {
			continue
		}
		targets := []string{
			filepath.Join(p.aiTeam, "state", "runs", runID+".json"),
			filepath.Join(p.aiTeam, "state", "candidates", runID+".json"),
		}
		for _, path := range targets {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				p.plan.Skipped = append(p.plan.Skipped, Skipped{
					Path: path, Reason: "state file должен быть regular file без symlink",
				})
				continue
			}
			p.plan.Actions = append(p.plan.Actions, Action{
				Category: CategoryState, Path: path, Bytes: info.Size(),
			})
		}
		approvals := filepath.Join(p.aiTeam, "state", "approvals", runID)
		if info, err := os.Lstat(approvals); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			size, sizeErr := directorySize(approvals)
			if sizeErr != nil {
				p.plan.Skipped = append(p.plan.Skipped, Skipped{
					Path: approvals, Reason: fmt.Sprintf("не удалось измерить: %v", sizeErr),
				})
			} else {
				p.plan.Actions = append(p.plan.Actions, Action{
					Category: CategoryState, Path: approvals, Bytes: size,
				})
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		} else if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: approvals, Reason: "approvals должен быть каталогом без symlink",
			})
		}
	}
	return nil
}

// planDatabaseApprovals plans per-run deletion from the SQLite approval table
// using the same terminal/age/keep-last rules as file-backed approvals.
func (p *planner) planDatabaseApprovals() error {
	if err := validateApprovalDBPath(p.options.Target, p.approvalDB); err != nil {
		return fmt.Errorf("approval DB path: %w", err)
	}
	info, err := os.Lstat(p.approvalDB)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		p.plan.Skipped = append(p.plan.Skipped, Skipped{Path: p.approvalDB, Reason: "approval DB должен быть regular file без symlink"})
		return nil
	}
	db, err := sql.Open("sqlite", p.approvalDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var tableCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='approval_records'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return nil
	}
	for runID := range p.terminal {
		if !p.eligibleForAge(runID) {
			continue
		}
		var count, logicalBytes int64
		if err := db.QueryRow(`SELECT count(*), COALESCE(sum(length(CAST(record_json AS BLOB))),0) FROM approval_records WHERE run_id=?`, runID).Scan(&count, &logicalBytes); err != nil {
			return err
		}
		if count > 0 {
			p.plan.Actions = append(p.plan.Actions, Action{Category: CategoryApprovals, Path: p.approvalDB, RunID: runID, LogicalBytes: logicalBytes})
		}
	}
	return nil
}

func deleteRunApprovals(path, runID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM approval_records WHERE run_id=?`, runID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// validateApprovalDBPath checks every existing component below .ai-team with
// Lstat. Lexical containment alone is insufficient because a parent symlink
// can redirect SQLite to a database outside the target.
func validateApprovalDBPath(target, path string) error {
	root, err := safeio.ExistingDir(target, ".ai-team")
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, absPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("approval DB path %s is outside %s", path, root)
	}
	current := root
	components := strings.Split(relative, string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("invalid approval DB path component %q", component)
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("approval DB path contains symlink component %s", current)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("approval DB parent %s is not a directory", current)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("approval DB %s must be a regular file", current)
		}
	}
	return nil
}

// planRuns: immutable evidence удаляется только когда вызывающий явно
// включил PruneRuns, раны terminal, старше older-than, вне keep-last И их
// portable export проверенно зафиксирован в state/exports (V0-0 guard).
// Отсутствие verified-export записи — fail-closed: evidence не удаляется.
func (p *planner) planRuns() error {
	root := filepath.Join(p.aiTeam, "runs")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		runID := entry.Name()
		path := filepath.Join(root, runID)
		if entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "run evidence должен быть каталогом без symlink",
			})
			continue
		}
		if !p.isTerminal(runID) {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "run ещё non-terminal",
			})
			continue
		}
		if !p.eligibleForAge(runID) {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "свежий terminal run (older-than или keep-last)",
			})
			continue
		}
		if !p.exports[runID] {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "evidence не проверенно экспортирована (нет verified-записи в state/exports; V0-4)",
			})
			continue
		}
		size, sizeErr := directorySize(path)
		if sizeErr != nil {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: fmt.Sprintf("не удалось измерить: %v", sizeErr),
			})
			continue
		}
		p.plan.Actions = append(p.plan.Actions, Action{
			Category: CategoryRuns, Path: path, Bytes: size,
		})
	}
	return nil
}

func (p *planner) isTerminal(runID string) bool {
	_, ok := p.terminal[runID]
	return ok
}

func (p *planner) lifecyclePhase(runID string) (string, bool) {
	path := filepath.Join(p.aiTeam, "state", "runs", runID+".json")
	if _, err := os.Lstat(path); err != nil {
		return "", false
	}
	return "", true
}

type lifecycleSnapshot struct {
	runID     string
	phase     string
	updatedAt time.Time
}

// readLifecycle разбирает lifecycle state терпимо к будущим полям: для целей
// gc важны только phase и updated_at.
func readLifecycle(path string) (lifecycleSnapshot, error) {
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return lifecycleSnapshot{}, err
	}
	var raw struct {
		RunID     string    `json:"run_id"`
		Phase     string    `json:"phase"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return lifecycleSnapshot{}, err
	}
	return lifecycleSnapshot{runID: raw.RunID, phase: raw.Phase, updatedAt: raw.UpdatedAt}, nil
}

// directorySize суммирует размеры regular files под root, отказываясь
// идти через symlinks и special files.
func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mode := entry.Type()
		if mode&fs.ModeSymlink != 0 || mode&(fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
			return fmt.Errorf("%s содержит symlink или special file", root)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// insideRoot гарантирует, что удаляемый путь лежит строго внутри control
// каталога .ai-team и не выходит наружу через "..".
func insideRoot(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("путь %s вне %s: %w", path, root, err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return fmt.Errorf("путь %s вне %s", path, root)
	}
	return nil
}

// removeGitWorktree отвязывает каталог от Git registry перед удалением,
// чтобы не оставлять висячих записей в .git/worktrees.
func removeGitWorktree(target, path string) error {
	command := exec.Command("git", "-C", target, "worktree", "remove", "--force", path)
	output, err := command.CombinedOutput()
	if err == nil {
		return os.RemoveAll(path)
	}
	// Каталог мог не быть зарегистрированным worktree (сирота в fixture,
	// уже отвязанный каталог или target вне Git) — тогда достаточно
	// обычного удаления каталога.
	message := strings.ToLower(string(output))
	if strings.Contains(message, "not a working tree") || strings.Contains(message, "not a git repository") {
		return os.RemoveAll(path)
	}
	return fmt.Errorf("git worktree remove %s: %s", path, strings.TrimSpace(string(output)))
}

// pruneGitWorktrees — best-effort очистка stale записей Git после удаления.
func pruneGitWorktrees(target string) error {
	command := exec.Command("git", "-C", target, "worktree", "prune")
	_, _ = command.CombinedOutput()
	return nil
}
