// Package ciimport импортирует ограниченный объяснимый набор checks из
// реального project CI (P1-8) БЕЗ исполнения произвольного YAML.
//
// Начальный adopter-формат — GitHub Actions workflow (".github/workflows/*.yml").
// Из workflow извлекаются только хорошо известные шаги (uses/run), которым
// соответствует детерминированное checks.Definition. Никакой shell/template
// исполнения и никаких произвольных команд: неизвестные шаги пропускаются
// (не импортируются), а не выполняются. Итоговая effective suite показывается
// и fingerprinted до запуска, чтобы adopter мог подтвердить состав набора.
package ciimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

// ImportFormat — поддерживаемый adopter-формат. Начинаем с одного формата.
type ImportFormat string

const (
	FormatGitHubActions ImportFormat = "github-actions"
)

// DefaultImportFormat — первый adopter-формат для P1-8.
const DefaultImportFormat = FormatGitHubActions

// WorkflowPath — стандартный путь workflow внутри target.
const WorkflowPath = ".github/workflows"

// SkippedStep фиксирует причину пропуска шага: он не импортируется и не
// исполняется (безопасность от произвольного YAML).
type SkippedStep struct {
	Source  string `json:"source"`
	Job     string `json:"job"`
	Step    int    `json:"step"`
	Reason  string `json:"reason"`
	Action  string `json:"action,omitempty"`
	Command string `json:"command,omitempty"`
}

// Imported — результат импорта: effective suite, отпечаток и пропуски.
type Imported struct {
	Format        ImportFormat        `json:"format"`
	Definitions   []checks.Definition `json:"checks"`
	Fingerprint   string              `json:"fingerprint"`
	SourceFiles   []string            `json:"source_files"`
	Skipped       []SkippedStep       `json:"skipped,omitempty"`
	WorkflowCount int                 `json:"workflow_count"`
}

// LoadWorkflows читает все workflow-файлы target'а (формат задаётся) и
// возвращает список путей. Только regular files, без follow симлинков.
func LoadWorkflows(targetDir string, format ImportFormat) ([]string, error) {
	switch format {
	case FormatGitHubActions:
		return loadGHAWorkflows(targetDir)
	default:
		return nil, fmt.Errorf("ciimport: неизвестный формат %q", format)
	}
}

func loadGHAWorkflows(targetDir string) ([]string, error) {
	dir := filepath.Join(targetDir, filepath.FromSlash(WorkflowPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ciimport: read workflows: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)
	return paths, nil
}

// Import читает и преобразует workflow'ы формата format из targetDir в
// effective suite. Все шаги проходят строгое преобразование; неизвестные —
// в Skipped. Итог fingerprint'ится.
func Import(targetDir string, format ImportFormat) (Imported, error) {
	paths, err := LoadWorkflows(targetDir, format)
	if err != nil {
		return Imported{}, err
	}
	imp := Imported{Format: format}
	for _, p := range paths {
		workflows, errs := parseWorkflow(p)
		for _, w := range workflows {
			imp.WorkflowCount++
			imp.applyWorkflow(p, w)
		}
		for _, e := range errs {
			imp.Skipped = append(imp.Skipped, SkippedStep{Source: p, Reason: e.Error()})
		}
	}
	imp.SourceFiles = paths
	dedupeSorted(&imp)
	imp.Fingerprint = Fingerprint(imp.Definitions, paths)
	return imp, nil
}

func (imp *Imported) applyWorkflow(path string, w workflow) {
	// Детерминированный порядок: сортировка id job'ов (map) — для стабильного
	// набора и fingerprint'а (порядок steps внутри job сохраняется YAML).
	jobIDs := make([]string, 0, len(w.Jobs))
	for id := range w.Jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	for _, jobID := range jobIDs {
		job := w.Jobs[jobID]
		for i, step := range job.Steps {
			stepIndex := i + 1
			src := filepath.ToSlash(path)
			if def, ok := mapStep(step); ok {
				imp.Definitions = append(imp.Definitions, def)
				continue
			}
			sk := SkippedStep{Source: src, Job: jobID, Step: stepIndex,
				Action: step.Uses, Command: step.Run}
			switch {
			case step.Uses != "" && step.Run != "":
				sk.Reason = "шаг с одновременными uses и run не отображается (неоднозначно)"
			case step.Uses == "" && step.Run == "":
				sk.Reason = "шаг без uses/run игнорируется"
			default:
				sk.Reason = "шаг не входит в ограниченное объяснимое сопоставление"
			}
			imp.Skipped = append(imp.Skipped, sk)
		}
	}
}

func dedupeSorted(imp *Imported) {
	byName := make(map[string]bool, len(imp.Definitions))
	out := make([]checks.Definition, 0, len(imp.Definitions))
	for _, d := range imp.Definitions {
		if byName[d.Name] {
			continue
		}
		byName[d.Name] = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Name < out[j].Name
	})
	imp.Definitions = out
}

// Fingerprint возвращает детерминированный SHA-256 по каноническому набору
// definitions и списку исходных файлов. Определяется ДО запуска checks.
func Fingerprint(definitions []checks.Definition, sources []string) string {
	payload := struct {
		Definitions []checks.Definition `json:"checks"`
		Sources     []string            `json:"sources"`
	}{Definitions: definitions, Sources: sources}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
