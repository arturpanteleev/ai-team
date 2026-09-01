package ciimport

import (
	"fmt"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"gopkg.in/yaml.v3"
)

type workflow struct {
	Jobs map[string]job `yaml:"jobs"`
}

type job struct {
	Steps []step `yaml:"steps"`
}

type step struct {
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

// parseWorkflow строго-читает workflow-файл, но только те поля, которые
// участвуют в ограниченном сопоставлении (jobs[].steps[].uses/run). Остальное
// workflow игнорируется; произвольные step-поля (shell, env, template) НЕ
// читаются и никогда не выполняются. Cancelled/условные шаги не учитываются.
// Возвращает nil при malformed YAML (одна ошибка в списке).
func parseWorkflow(path string) ([]workflow, []error) {
	data, err := safeio.ReadRegularFile(path, 4<<20)
	if err != nil {
		return nil, []error{err}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, []error{fmt.Errorf("%s: %w", path, err)}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, []error{fmt.Errorf("%s: пустой workflow", path)}
	}
	workflowsNode := root.Content[0]
	if workflowsNode.Kind != yaml.MappingNode {
		return nil, []error{fmt.Errorf("%s: workflow должен быть mapping", path)}
	}
	var wf workflow
	// Обрабатываем только ключ "jobs"; остальные верхнеуровневые игнорируем.
	if err := decodeField(workflowsNode, "jobs", &wf.Jobs); err != nil {
		return nil, []error{err}
	}
	return []workflow{wf}, nil
}

func decodeField(mapping *yaml.Node, key string, out interface{}) error {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			if err := mapping.Content[i+1].Decode(out); err != nil {
				return err
			}
			return nil
		}
	}
	return nil
}

// mapStep преобразует один шаг в checks.Definition. Второе значение false —
// шаг не входит в ограниченное объяснимое сопоставление и НЕ импортируется.
func mapStep(s step) (checks.Definition, bool) {
	if s.Uses == "" && s.Run == "" {
		return checks.Definition{}, false
	}
	if s.Uses != "" && s.Run != "" {
		return checks.Definition{}, false
	}
	if s.Uses != "" {
		return mapUses(s.Uses)
	}
	return mapRun(s.Run)
}

// mapUses отображает известные composite/action uses-идентификаторы. Только
// явный whitelist; версии игнорируются (не исполняются — это label).
func mapUses(uses string) (checks.Definition, bool) {
	id := strings.ToLower(uses)
	switch {
	case strings.HasPrefix(id, "golangci/golangci-lint-action"):
		return checks.Definition{
			Name:    "golangci-lint",
			Class:   "lint",
			Adapter: checks.AdapterCommand,
			Command: []string{"golangci-lint", "run"},
			Policy:  checks.PolicyOptional,
		}, true
	case strings.HasPrefix(id, "github/codeql-action"):
		return checks.Definition{
			Name:    "codeql-analyze",
			Class:   "security",
			Adapter: checks.AdapterCommand,
			Command: []string{"codeql", "database", "analyze"},
			Policy:  checks.PolicyOptional,
		}, true
	default:
		return checks.Definition{}, false
	}
}

// mapRun отображает ограниченный набор well-known `run:` shell-команд.
// Никакие переменные/шаблоны не раскрываются — только статический префикс.
// Многокомандные/операторные run (&&, ||, ;, |) и многострочные не
// отображаются (мы не парсим shell) и НЕ импортируются.
func mapRun(run string) (checks.Definition, bool) {
	cmd := strings.TrimSpace(run)
	if strings.ContainsAny(cmd, "&\n|;") {
		return checks.Definition{}, false
	}
	switch {
	case strings.HasPrefix(cmd, "go vet"):
		return checks.Definition{
			Name:    "go-vet",
			Class:   "lint",
			Adapter: checks.AdapterCommand,
			Command: []string{"go", "vet"},
			Policy:  checks.PolicyOptional,
		}, true
	case strings.HasPrefix(cmd, "go build"):
		return checks.Definition{
			Name:    "go-build",
			Class:   "build",
			Adapter: checks.AdapterCommand,
			Command: []string{"go", "build"},
			Policy:  checks.PolicyOptional,
		}, true
	case strings.HasPrefix(cmd, "go test"):
		return mapGoTest(cmd)
	default:
		return checks.Definition{}, false
	}
}

func mapGoTest(cmd string) (checks.Definition, bool) {
	args := strings.Fields(cmd)
	// Класс остаётся unit для go-test-json adapter'а (он ограничен тестовыми
	// классами); опция -race остаётся в команде — race-запуск детектируется по
	// command, а не по class.
	hasJSON := containsWord(args, "-json")
	if hasJSON {
		return checks.Definition{
			Name:    "go-test",
			Class:   "unit",
			Adapter: checks.AdapterGoTest,
			Command: args,
			Policy:  checks.PolicyOptional,
		}, true
	}
	// Вставляем -json, чтобы использовать go-test-json adapter (требует его).
	withJSON := append([]string(nil), args...)
	withJSON = append(withJSON, "-json")
	return checks.Definition{
		Name:    "go-test",
		Class:   "unit",
		Adapter: checks.AdapterGoTest,
		Command: withJSON,
		Policy:  checks.PolicyOptional,
	}, true
}

func containsWord(fields []string, word string) bool {
	for _, f := range fields {
		if f == word {
			return true
		}
	}
	return false
}
