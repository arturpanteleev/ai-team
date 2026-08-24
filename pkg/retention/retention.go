// Package retention строит план уборки растущих артефактов .ai-team:
// candidate worktrees, mutable control state и (только по явному флагу)
// immutable run evidence.
package retention

import (
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
)

const (
	CategoryWorktrees = "worktrees"
	CategoryRuns      = "runs"
	CategoryState     = "state"
)

// Options описывает параметры одного прохода gc.
type Options struct {
	Target    string
	OlderThan time.Duration
	KeepLast  int
	PruneRuns bool
}

// Action — один объект, подлежащий удалению.
type Action struct {
	Category string
	Path     string
	Bytes    int64
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

type terminalRun struct {
	runID     string
	updatedAt time.Time
}

type planner struct {
	options  Options
	aiTeam   string
	now      time.Time
	plan     Plan
	terminal map[string]time.Time
	keepSet  map[string]bool
	cutoff   time.Duration
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
	p := &planner{
		options:  options,
		aiTeam:   aiTeam,
		now:      time.Now().UTC(),
		terminal: map[string]time.Time{},
		keepSet:  map[string]bool{},
		cutoff:   options.OlderThan,
	}
	if err := p.loadTerminalRuns(); err != nil {
		return nil, err
	}
	p.markKeepLast()
	if err := p.planWorktrees(); err != nil {
		return nil, err
	}
	if err := p.planState(); err != nil {
		return nil, err
	}
	if options.PruneRuns {
		if err := p.planRuns(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(p.plan.Actions, func(i, j int) bool {
		order := map[string]int{CategoryWorktrees: 0, CategoryRuns: 1, CategoryState: 2}
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

// planRuns: immutable evidence удаляется только когда вызывающий явно
// включил PruneRuns, раны terminal, старше older-than и вне keep-last.
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
