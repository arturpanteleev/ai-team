// Package gate реализует `ai-team gate` MVP (V0-5): детерминированный diff-
// policy вердикт по trusted local base/candidate, typed checks (переиспользует
// pkg/checks Runner с evidence digests) и самодостаточный attestation bundle со
// стабильными exit codes. Gate работает без .ai-team и без runtime: это дешёвый
// вход для внешних проектов. untrusted mode (сетевые refs/нелокальное
// содержимое) запрещён до P1-4 — gate всегда fail-closed при невозможности
// разрешить ref в локальный объект.
package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/risk"
	"github.com/arturpanteleev/ai-team/pkg/safeio"

	"github.com/arturpanteleev/ai-team/pkg/scope"
	"gopkg.in/yaml.v3"
)

// SchemaVersion — версия gate config и attestation bundle.
const SchemaVersion = 1

// BundleType — тип index.json attestation bundle.
const BundleType = "ai-team-gate-bundle"

const indexFileName = "index.json"

// Стабильные exit codes gate. Одинаковые причины всегда дают один код.
const (
	ExitPass    = 0
	ExitFail    = 1
	ExitBlocked = 2
)

// Значения diff-policy test_modify.
const (
	TestModifyRequired = "required"
	TestModifyWarning  = "warning"
	TestModifyOff      = "off"
)

// Кинды мутаций diff между base и candidate.
const (
	KindAdded    = "added"
	KindModified = "modified"
	KindRemoved  = "removed"
)

// Вердикты policy.
const (
	VerdictPassed   = "passed"
	VerdictViolated = "violated"
	VerdictWarning  = "warning"
	VerdictSkipped  = "skipped"
)

const (
	maxConfigSize = 1 << 20
	maxIndexSize  = 1 << 20
)

// DiffPolicy — политика на типизированные мутации (V0-1 semantics).
type DiffPolicy struct {
	TestModify string `yaml:"test_modify" json:"test_modify"`
}

// UnmarshalYAML строго валидирует mapping diff_policy.
func (p *DiffPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("gate config: diff_policy должен быть mapping")
	}
	if err := validateMappingKeys(node, map[string]bool{"test_modify": true}, "gate config: diff_policy"); err != nil {
		return err
	}
	type plain DiffPolicy
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*p = DiffPolicy(decoded)
	switch p.TestModify {
	case TestModifyRequired, TestModifyWarning, TestModifyOff:
		return nil
	}
	return fmt.Errorf("gate config: diff_policy.test_modify должен быть %q, %q или %q",
		TestModifyRequired, TestModifyWarning, TestModifyOff)
}

// Config — строгая конфигурация gate.
type Config struct {
	SchemaVersion int                 `yaml:"schema_version" json:"schema_version"`
	DiffPolicy    DiffPolicy          `yaml:"diff_policy" json:"diff_policy"`
	Checks        []checks.Definition `yaml:"checks,omitempty" json:"checks,omitempty"`
}

// UnmarshalYAML отбрасывает неизвестные поля и требует корректную схему.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("gate config: должен быть mapping")
	}
	if err := validateMappingKeys(node, map[string]bool{
		"schema_version": true, "diff_policy": true, "checks": true,
	}, "gate config"); err != nil {
		return err
	}
	type rawConfig struct {
		SchemaVersion int                 `yaml:"schema_version"`
		DiffPolicy    DiffPolicy          `yaml:"diff_policy"`
		Checks        []checks.Definition `yaml:"checks"`
	}
	var raw rawConfig
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.SchemaVersion != SchemaVersion {
		return fmt.Errorf("gate config: schema_version должен быть %d", SchemaVersion)
	}
	if raw.DiffPolicy.TestModify == "" {
		return fmt.Errorf("gate config: diff_policy.test_modify обязателен")
	}
	seen := make(map[string]bool)
	for _, definition := range raw.Checks {
		if definition.Name == "" {
			return fmt.Errorf("gate config: check name обязателен")
		}
		if seen[definition.Name] {
			return fmt.Errorf("gate config: дублирующийся check %q", definition.Name)
		}
		seen[definition.Name] = true
	}
	c.SchemaVersion = raw.SchemaVersion
	c.DiffPolicy = raw.DiffPolicy
	c.Checks = raw.Checks
	return nil
}

// LoadConfig парсит gate config с диска.
func LoadConfig(path string) (*Config, error) {
	data, err := safeio.ReadRegularFile(path, maxConfigSize)
	if err != nil {
		return nil, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("gate config %s: %w", path, err)
	}
	return &cfg, nil
}

func validateMappingKeys(node *yaml.Node, allowed map[string]bool, where string) error {
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", where, key)
		}
		seen[key] = true
		if !allowed[key] {
			return fmt.Errorf("%s: unknown field %q", where, key)
		}
	}
	return nil
}

// Mutation — одна типизированная мутация diff.
type Mutation struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Kind    string `json:"kind"`
	Class   string `json:"class"`
}

// BlockedError — fail-closed причина (недоступный локальный ref, unsafe
// состояние, запрещённый untrusted mode). Отображается в exit code 2.
type BlockedError struct {
	Reason string
}

func (e *BlockedError) Error() string { return "BLOCKED: " + e.Reason }

// Options — вход Run.
type Options struct {
	TargetDir      string
	Base           string
	Candidate      string
	Config         *Config
	AllowUntrusted bool
	// Receipt (V0-P1-4) — containment receipt for the gating run. When absent,
	// untrusted mode is blocked (fail-closed). Present + no UNAVAILABLE axes →
	// untrusted allowed.
	Receipt *containment.Receipt
}

// Result — детерминированный вердикт gate (за исключением FinishedAt).
type Result struct {
	SchemaVersion    int             `json:"schema_version"`
	Base             string          `json:"base"`
	Candidate        string          `json:"candidate"`
	BaseCommit       string          `json:"base_commit"`
	CandidateCommit  string          `json:"candidate_commit,omitempty"`
	BaseTree         string          `json:"base_tree"`
	CandidateTree    string          `json:"candidate_tree,omitempty"`
	DiffPolicy       string          `json:"diff_policy_test_modify"`
	Mutations        []Mutation      `json:"mutations"`
	PolicyVerdict    string          `json:"policy_verdict"`
	PolicyViolations []Mutation      `json:"policy_violations,omitempty"`
	Checks           []checks.Result `json:"checks"`
	Signals          Signals         `json:"signals"`
	Status           string          `json:"status"`
	FinishedAt       time.Time       `json:"finished_at"`
	BundleSHA256     string          `json:"bundle_sha256,omitempty"`
}

// Signals (V0-8) — измеренные risk-signals diff: размер/тип изменений,
// чувствительные пути, тестовые изменения, failed checks. Записываются в
// bundle как данные и НЕ маршрутизируют pipeline (routing — позже, P2-1/P2-2).
type Signals struct {
	AddedFiles     int          `json:"added_files"`
	ModifiedFiles  int          `json:"modified_files"`
	RemovedFiles   int          `json:"removed_files"`
	AddedLines     int64        `json:"added_lines"`
	RemovedLines   int64        `json:"removed_lines"`
	TestChanges    int          `json:"test_changes"`
	ChecksRun      int          `json:"checks_run"`
	FailedChecks   int          `json:"failed_checks"`
	SensitivePaths []risk.Entry `json:"sensitive_paths,omitempty"`
}

// Run выполняет diff-policy проверку и typed checks для trusted local
// base/candidate. Возвращает вердикт, exit code и ошибку (только для
// BLOCKED — infra/config; fail по вердикту отражён в exit code 0/1).
func Run(ctx context.Context, opt Options) (*Result, int, error) {
	if opt.TargetDir == "" {
		opt.TargetDir = "."
	}
	if opt.Base == "" {
		opt.Base = "HEAD"
	}
	if opt.Candidate == "" {
		opt.Candidate = "WORKTREE"
	}
	if len(opt.Base) > 1024 || len(opt.Candidate) > 1024 {
		return nil, ExitBlocked, &BlockedError{Reason: "ref слишком длинный"}
	}
	if opt.AllowUntrusted && opt.Receipt == nil {
		return nil, ExitBlocked, &BlockedError{Reason: "untrusted mode требует containment receipt (--allow-untrusted)"}
	}
	if opt.AllowUntrusted && opt.Receipt.HasUnavailable() {
		return nil, ExitBlocked, &BlockedError{Reason: "untrusted mode запрещён: оси containment UNAVAILABLE"}
	}
	cfg := opt.Config
	if cfg == nil {
		cfg = &Config{SchemaVersion: SchemaVersion, DiffPolicy: DiffPolicy{TestModify: TestModifyRequired}}
	}
	if cfg.DiffPolicy.TestModify == "" {
		cfg.DiffPolicy.TestModify = TestModifyRequired
	}

	worktree, err := isWorkTree(ctx, opt.TargetDir)
	if err != nil {
		return nil, ExitBlocked, err
	}
	if !worktree {
		return nil, ExitBlocked, &BlockedError{Reason: "target не является Git working tree"}
	}

	baseCommit, err := resolveCommit(ctx, opt.TargetDir, opt.Base)
	if err != nil {
		return nil, ExitBlocked, &BlockedError{Reason: err.Error()}
	}
	candidate := opt.Candidate
	candidateCommit := ""
	worktreeMode := candidate == "WORKTREE" || candidate == ""
	if !worktreeMode {
		candidateCommit, err = resolveCommit(ctx, opt.TargetDir, candidate)
		if err != nil {
			return nil, ExitBlocked, &BlockedError{Reason: err.Error()}
		}
	}
	baseTree, err := treeSHA(ctx, opt.TargetDir, baseCommit)
	if err != nil {
		return nil, ExitBlocked, &BlockedError{Reason: err.Error()}
	}
	candidateTree := ""
	if worktreeMode {
		candidateTree = workingTreeSHA(ctx, opt.TargetDir)
	} else {
		candidateTree, err = treeSHA(ctx, opt.TargetDir, candidateCommit)
		if err != nil {
			return nil, ExitBlocked, &BlockedError{Reason: err.Error()}
		}
	}

	diff, err := diffNameStatus(ctx, opt.TargetDir, baseCommit, candidateCommit, worktreeMode)
	if err != nil {
		return nil, ExitBlocked, &BlockedError{Reason: err.Error()}
	}
	mutations := classifyMutations(diff)

	result := &Result{
		SchemaVersion:   SchemaVersion,
		Base:            opt.Base,
		Candidate:       candidate,
		BaseCommit:      baseCommit,
		CandidateCommit: candidateCommit,
		BaseTree:        baseTree,
		CandidateTree:   candidateTree,
		DiffPolicy:      cfg.DiffPolicy.TestModify,
		Mutations:       mutations,
		FinishedAt:      time.Now().UTC(),
	}

	violations, verdict := policyVerdict(cfg.DiffPolicy.TestModify, mutations)
	result.PolicyVerdict = verdict
	result.PolicyViolations = violations

	addLines, remLines, numstatErr := diffNumstatLines(ctx, opt.TargetDir, baseCommit, candidateCommit, worktreeMode)
	if numstatErr != nil {
		return nil, ExitBlocked, &BlockedError{Reason: "не удалось измерить размер diff (numstat): " + numstatErr.Error()}
	}
	signalPaths := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		signalPaths = append(signalPaths, mutation.Path)
	}
	result.Signals = Signals{
		AddedLines:     addLines,
		RemovedLines:   remLines,
		SensitivePaths: risk.Entries(signalPaths),
	}
	for _, mutation := range mutations {
		switch mutation.Kind {
		case KindAdded:
			result.Signals.AddedFiles++
		case KindModified:
			result.Signals.ModifiedFiles++
		case KindRemoved:
			result.Signals.RemovedFiles++
		}
		if mutation.Class == scope.ClassTests && (mutation.Kind == KindAdded || mutation.Kind == KindModified) {
			result.Signals.TestChanges++
		}
	}

	if len(cfg.Checks) > 0 {
		results, checkErr := (checks.Runner{TargetDir: opt.TargetDir}).RunAll(ctx, cfg.Checks)
		result.Checks = results
		result.Signals.ChecksRun = len(results)
		for _, check := range results {
			if check.Status == checks.StatusFailed {
				result.Signals.FailedChecks++
			}
		}
		if checkErr != nil {
			result.Status = "failed"
			return result, ExitFail, nil
		}
	}

	switch result.PolicyVerdict {
	case VerdictViolated:
		result.Status = "failed"
		return result, ExitFail, nil
	}
	result.Status = "passed"
	return result, ExitPass, nil
}

// diffNumstatLines измеряет размер diff в добавленных/удалённых строках через
// `git diff --numstat`. Бинарные файлы выводятся как "-" и учитываются как 0
// строк (факт их наличия уже покрыт file counts в Signals).
func diffNumstatLines(ctx context.Context, target, baseCommit, candidateCommit string, worktreeMode bool) (added, removed int64, err error) {
	args := []string{"-C", target, "--no-pager", "diff", "--numstat", baseCommit}
	if !worktreeMode {
		args = append(args, candidateCommit)
	}
	command := exec.CommandContext(ctx, "git", args...)
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	if err := command.Run(); ctx.Err() != nil {
		return 0, 0, ctx.Err()
	} else if err != nil {
		return 0, 0, errors.New(strings.TrimSpace(buffer.String()))
	}
	for _, line := range strings.Split(buffer.String(), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		add := parseIntOrZero(fields[0])
		rem := parseIntOrZero(fields[1])
		added += add
		removed += rem
	}
	return added, removed, nil
}

func parseIntOrZero(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func isWorkTree(ctx context.Context, target string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", target, "rev-parse", "--is-inside-work-tree")
	output, err := command.Output()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, &BlockedError{Reason: fmt.Sprintf("не удалось инициализировать Git: %v", err)}
	}
	return strings.TrimSpace(string(output)) == "true", nil
}

func resolveCommit(ctx context.Context, target, ref string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", target, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return "", fmt.Errorf("ref %q не разрешается в локальный commit; gate работает только с trusted local содержимым (сетевое/отсутствующее содержимое запрещено до P1-4)", ref)
	}
	return strings.TrimSpace(string(output)), nil
}

func treeSHA(ctx context.Context, target, commit string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", target, "rev-parse", commit+"^{tree}")
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// workingTreeSHA — короткая локальная метка для WORKTREE-кандидата: git
// не даёт стабильного sha для рабочего дерева, поэтому используется
// stat-метка, детерминированная от содержимого (ls-files + checksums не
// расширяют контракт); достаточна для attestation context run'а.
func workingTreeSHA(ctx context.Context, target string) string {
	command := exec.CommandContext(ctx, "git", "-C", target, "status", "--porcelain")
	output, err := command.Output()
	if ctx.Err() != nil {
		return "?"
	}
	if err != nil {
		return "?"
	}
	sum := sha256.Sum256(output)
	return hex.EncodeToString(sum[:])
}

type diffEntry struct {
	kind    byte
	oldPath string
	newPath string
}

func diffNameStatus(ctx context.Context, target, baseCommit, candidateCommit string, worktreeMode bool) ([]diffEntry, error) {
	args := []string{"-C", target, "--no-pager", "diff", "--name-status", "-z", "-M", baseCommit}
	if !worktreeMode {
		args = append(args, candidateCommit)
	}
	command := exec.CommandContext(ctx, "git", args...)
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	if err := command.Run(); ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		return nil, errors.New(strings.TrimSpace(buffer.String()))
	}
	tokens := strings.Split(buffer.String(), "\x00")
	entries := make([]diffEntry, 0, len(tokens)/2)
	i := 0
	for i < len(tokens) {
		status := tokens[i]
		i++
		if status == "" {
			continue
		}
		kind := status[0]
		switch kind {
		case 'A', 'M', 'D', 'T':
			if i >= len(tokens) {
				return nil, errors.New("diff: повреждённый name-status вывод")
			}
			entries = append(entries, diffEntry{kind: kind, newPath: tokens[i]})
			i++
		case 'R', 'C':
			if i+1 >= len(tokens) {
				return nil, errors.New("diff: повреждённый rename name-status вывод")
			}
			entries = append(entries, diffEntry{kind: kind, oldPath: tokens[i], newPath: tokens[i+1]})
			i += 2
		}
	}
	return entries, nil
}

func classifyMutations(entries []diffEntry) []Mutation {
	mutations := make([]Mutation, 0, len(entries))
	for _, entry := range entries {
		switch entry.kind {
		case 'A':
			mutations = append(mutations, Mutation{Kind: KindAdded, Path: entry.newPath, Class: scope.ClassifyMutation(entry.newPath)})
		case 'M', 'T':
			mutations = append(mutations, Mutation{Kind: KindModified, Path: entry.newPath, Class: scope.ClassifyMutation(entry.newPath)})
		case 'D':
			mutations = append(mutations, Mutation{Kind: KindRemoved, Path: entry.newPath, Class: scope.ClassifyMutation(entry.newPath)})
		case 'R':
			mutations = append(mutations,
				Mutation{Kind: KindRemoved, Path: entry.oldPath, Class: scope.ClassifyMutation(entry.oldPath)},
				Mutation{Kind: KindAdded, Path: entry.newPath, Class: scope.ClassifyMutation(entry.newPath)})
		case 'C':
			mutations = append(mutations, Mutation{Kind: KindAdded, Path: entry.newPath, Class: scope.ClassifyMutation(entry.newPath)})
		}
	}
	sort.Slice(mutations, func(i, j int) bool {
		if mutations[i].Path != mutations[j].Path {
			return mutations[i].Path < mutations[j].Path
		}
		return mutations[i].OldPath < mutations[j].OldPath
	})
	return mutations
}

// policyVerdict применяет diff-policy test_modify (V0-1 semantics): изменение
// source без сменящих его тестов (added/modified) — нарушение.
func policyVerdict(policy string, mutations []Mutation) ([]Mutation, string) {
	if policy == TestModifyOff {
		return nil, VerdictSkipped
	}
	hasTestChange := false
	for _, mutation := range mutations {
		if mutation.Class == scope.ClassTests && (mutation.Kind == KindAdded || mutation.Kind == KindModified) {
			hasTestChange = true
			break
		}
	}
	violations := make([]Mutation, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.Class == scope.ClassSource &&
			(mutation.Kind == KindAdded || mutation.Kind == KindModified) && !hasTestChange {
			violations = append(violations, mutation)
		}
	}
	if len(violations) == 0 {
		return nil, VerdictPassed
	}
	switch policy {
	case TestModifyRequired:
		return violations, VerdictViolated
	case TestModifyWarning:
		return violations, VerdictWarning
	}
	return nil, VerdictSkipped
}

// Index — детерминированный манифест attestation bundle (без тайм-меток).
type Index struct {
	SchemaVersion int      `json:"schema_version"`
	Type          string   `json:"type"`
	Base          string   `json:"base"`
	Candidate     string   `json:"candidate"`
	Records       []Record `json:"records"`
}

// Record — один файл bundle и его sha256.
type Record struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// BundleDigest — deterministic identity gate bundle: sha256 канонического
// index.json (одинаков только для идентичного набора records).
func BundleDigest(index *Index) string {
	data, err := json.Marshal(index)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

const maxRecordSize = 16 << 20 // 16 MiB на record файл при верификации

// VerifyBundle самодостаточно проверяет gate attestation bundle (V0-7): index.json
// каноничен и детерминирован, каждый заявленный record существует и совпадает по
// sha256, лишних файлов (не описанных в index) нет, заявленный bundle_sha256 в
// gate.json либо отсутствует, либо совпадает с пересчитанным digest. Работает без
// repo и .ai-team — bundle является единственным источником данных. Возвращает
// детерминированный digest bundle.
func VerifyBundle(bundleDir string) (string, error) {
	dir, err := safeio.ExistingDir(bundleDir)
	if err != nil {
		return "", fmt.Errorf("verify gate bundle: %v", err)
	}
	if err := safeio.ValidateTree(dir); err != nil {
		return "", fmt.Errorf("verify gate bundle: %v", err)
	}
	indexData, err := safeio.ReadRegularFile(filepath.Join(dir, indexFileName), maxIndexSize)
	if err != nil {
		return "", fmt.Errorf("verify gate bundle: index.json: %v", err)
	}
	var index Index
	if err := strictDecode(indexData, &index); err != nil {
		return "", fmt.Errorf("verify gate bundle: index.json: %v", err)
	}
	if index.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("verify gate bundle: несовместимая schema_version %d", index.SchemaVersion)
	}
	if index.Type != BundleType {
		return "", fmt.Errorf("verify gate bundle: неизвестный тип bundle %q", index.Type)
	}
	if index.Base == "" || index.Candidate == "" {
		return "", errors.New("verify gate bundle: отсутствует base/candidate identity в index.json")
	}
	expected := make(map[string]string, len(index.Records))
	for _, record := range index.Records {
		cleaned := filepath.Clean(filepath.FromSlash(record.Path))
		if record.Path == "" || filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." ||
			strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("verify gate bundle: недопустимый record path %q", record.Path)
		}
		if _, dup := expected[record.Path]; dup {
			return "", fmt.Errorf("verify gate bundle: дублирующий record %q", record.Path)
		}
		if len(record.SHA256) != sha256BytesLen {
			return "", fmt.Errorf("verify gate bundle: некорректный sha256 для %q", record.Path)
		}
		if _, hexErr := hex.DecodeString(record.SHA256); hexErr != nil {
			return "", fmt.Errorf("verify gate bundle: некорректный sha256 для %q", record.Path)
		}
		expected[record.Path] = record.SHA256
	}
	actual := map[string]bool{}
	err = filepath.WalkDir(dir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, current)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if slashed == indexFileName {
			return nil
		}
		if _, ok := expected[slashed]; !ok {
			return fmt.Errorf("verify gate bundle: лишний файл %q (отсутствует в index.json)", slashed)
		}
		actual[slashed] = true
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("verify gate bundle: %v", err)
	}
	if len(actual) != len(expected) {
		for path := range expected {
			if !actual[path] {
				return "", fmt.Errorf("verify gate bundle: record %q заявлен в index.json, но отсутствует", path)
			}
		}
	}
	for path, want := range expected {
		if !strings.HasPrefix(path, "checks/") && path != "gate.json" {
			return "", fmt.Errorf("verify gate bundle: record %q вне известных типов bundle", path)
		}
		data, readErr := safeio.ReadRegularFile(filepath.Join(dir, filepath.FromSlash(path)), maxRecordSize)
		if readErr != nil {
			return "", fmt.Errorf("verify gate bundle: %v", readErr)
		}
		if sha256Bytes(data) != want {
			return "", fmt.Errorf("verify gate bundle: record %q повреждён (sha256 не совпадает)", path)
		}
	}
	digest := BundleDigest(&index)
	gateData, readErr := safeio.ReadRegularFile(filepath.Join(dir, "gate.json"), maxRecordSize)
	if readErr != nil {
		return "", fmt.Errorf("verify gate bundle: %v", readErr)
	}
	var verdict Result
	if err := strictDecode(gateData, &verdict); err != nil {
		return "", fmt.Errorf("verify gate bundle: gate.json: %v", err)
	}
	// Cross-check: index.json обязан совпадать с идентичностью вердикта из
	// gate.json — иначе index описывает чужой bundle, а не лежащий на диске.
	candidate := verdict.CandidateCommit
	if candidate == "" {
		candidate = verdict.Candidate
	}
	if index.Base != verdict.BaseCommit || index.Candidate != candidate {
		return "", fmt.Errorf("verify gate bundle: index.json identity (base=%s candidate=%s) не совпадает с gate.json (base=%s candidate=%s)",
			index.Base, index.Candidate, verdict.BaseCommit, candidate)
	}
	if verdict.BundleSHA256 != "" && verdict.BundleSHA256 != digest {
		return "", fmt.Errorf("verify gate bundle: заявленный bundle_sha256 %q не равен digest %q",
			verdict.BundleSHA256, digest)
	}
	return digest, nil
}

const sha256BytesLen = 64

func strictDecode(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing data after JSON document")
		}
		return err
	}
	return nil
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// WriteBundle пишет самодостаточный attestation bundle в outDir:
// gate.json (полный вердикт), checks/<n>-<name>.json и index.json с sha256
// каждого record. Файлы immutable; index пишется последним. Digest
// возвращается через result.BundleSHA256.
func WriteBundle(outDir string, result *Result) error {
	if result == nil {
		return errors.New("gate: нет вердикта для bundle")
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	if _, err := safeio.ExistingDir(outDir); err != nil {
		return err
	}
	records := make([]Record, 0, len(result.Checks)+1)
	result.BundleSHA256 = ""
	gateData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	gateData = append(gateData, '\n')
	if err := os.WriteFile(filepath.Join(outDir, "gate.json"), gateData, 0444); err != nil {
		return err
	}
	records = append(records, Record{Type: "gate_result", Path: "gate.json", SHA256: sha256Bytes(gateData)})
	if len(result.Checks) > 0 {
		if err := os.MkdirAll(filepath.Join(outDir, "checks"), 0755); err != nil {
			return err
		}
		for i, check := range result.Checks {
			name := sanitizeName(check.Name)
			rel := filepath.Join("checks", fmt.Sprintf("%03d-%s.json", i+1, name))
			data, err := json.MarshalIndent(check, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
			if err := os.WriteFile(filepath.Join(outDir, rel), data, 0444); err != nil {
				return err
			}
			records = append(records, Record{Type: "check_result", Path: filepath.ToSlash(rel), SHA256: sha256Bytes(data)})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	// CandidateCommit пуст для WORKTREE-кандидата — identity bundle берётся из
	// запрошенного ref (BaseCommit всегда непуст после разрешения).
	candidate := result.CandidateCommit
	if candidate == "" {
		candidate = result.Candidate
	}
	index := &Index{
		SchemaVersion: SchemaVersion,
		Type:          BundleType,
		Base:          result.BaseCommit,
		Candidate:     candidate,
		Records:       records,
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, indexFileName), append(data, '\n'), 0644); err != nil {
		return err
	}
	result.BundleSHA256 = BundleDigest(index)
	return nil
}

func sanitizeName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	out := builder.String()
	if out == "" {
		return "check"
	}
	return out
}

// ExitCode переводит вердикт в стабильный exit code (без BLOCKED).
func ExitCode(result *Result) int {
	if result == nil || result.Status == "failed" {
		return ExitFail
	}
	return ExitPass
}
