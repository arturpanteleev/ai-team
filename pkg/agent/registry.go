package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/scope"
	"gopkg.in/yaml.v3"
)

type Registry struct {
	layers []Layer
	agents map[string]*Agent
}

// Layer is one complete agent-definition namespace. Layers are ordered from
// highest to lowest precedence; an invalid higher-precedence override fails
// closed instead of silently falling back.
type Layer struct {
	Name string
	FS   fs.FS
}

func NewRegistry(agentsDir string) *Registry {
	absDir, err := filepath.Abs(agentsDir)
	if err != nil {
		absDir = agentsDir
	}
	return &Registry{
		layers: []Layer{{Name: "filesystem", FS: os.DirFS(absDir)}},
		agents: make(map[string]*Agent),
	}
}

func NewFS(fsys fs.FS) *Registry {
	return NewLayered(Layer{Name: "embedded", FS: fsys})
}

func NewLayered(layers ...Layer) *Registry {
	filtered := make([]Layer, 0, len(layers))
	for i, layer := range layers {
		if layer.FS == nil {
			continue
		}
		if layer.Name == "" {
			layer.Name = fmt.Sprintf("layer-%d", i)
		}
		filtered = append(filtered, layer)
	}
	return &Registry{
		layers: filtered,
		agents: make(map[string]*Agent),
	}
}

func (r *Registry) Load(name string) (*Agent, error) {
	if a, ok := r.agents[name]; ok {
		return a, nil
	}

	var selected Layer
	var defData []byte
	var err error
	for _, layer := range r.layers {
		defData, err = fs.ReadFile(layer.FS, path.Join(name, "def.yaml"))
		if err == nil {
			selected = layer
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("агент %s: чтение layer %s: %w", name, layer.Name, err)
		}
	}
	if selected.FS == nil {
		return nil, fmt.Errorf("агент %s не найден ни в одном registry layer", name)
	}

	var a Agent
	decoder := yaml.NewDecoder(bytes.NewReader(defData))
	decoder.KnownFields(true)
	if err := decoder.Decode(&a); err != nil {
		return nil, fmt.Errorf("ошибка парсинга %s/def.yaml в layer %s: %w", name, selected.Name, err)
	}
	a.Source = selected.Name
	if err := validateDefinition(name, &a); err != nil {
		return nil, err
	}

	if a.PromptFile != "" {
		promptData, err := fs.ReadFile(selected.FS, path.Join(name, a.PromptFile))
		if err != nil {
			// prompt_file указан, но не читается — это ошибка конфигурации
			// агента, а не повод молча запустить его с пустым промптом
			return nil, fmt.Errorf("агент %s: prompt_file %s не читается: %w", name, a.PromptFile, err)
		}
		a.Prompt = string(promptData)
		if strings.TrimSpace(a.Prompt) == "" {
			return nil, fmt.Errorf("агент %s: prompt_file %s пуст", name, a.PromptFile)
		}
	}

	r.agents[name] = &a
	return &a, nil
}

func validateDefinition(dirName string, a *Agent) error {
	if a.Name == "" || a.Name != dirName {
		return fmt.Errorf("агент %s: поле name должно совпадать с каталогом", dirName)
	}
	if a.RuntimeType == "" {
		return fmt.Errorf("агент %s: runtime обязателен", dirName)
	}
	if a.Verdict != nil {
		if err := a.Verdict.Validate(); err != nil {
			return fmt.Errorf("агент %s: %w", dirName, err)
		}
	}
	switch a.Kind {
	case "", "agent", "delivery":
	default:
		return fmt.Errorf("агент %s: неизвестный kind %q", dirName, a.Kind)
	}
	if a.Mutation == "" {
		return fmt.Errorf("агент %s: mutation обязателен (none, source, tests или external)", dirName)
	}
	switch a.Mutation {
	case "none", "source", "tests", "external":
	default:
		return fmt.Errorf("агент %s: неизвестный mutation %q", dirName, a.Mutation)
	}
	if a.Kind == "delivery" && a.Mutation != "external" {
		return fmt.Errorf("агент %s: kind delivery требует mutation external", dirName)
	}
	if a.Mutation == "external" && a.Kind != "delivery" {
		return fmt.Errorf("агент %s: mutation external разрешён только для kind delivery", dirName)
	}
	if a.RequireDiff && a.Mutation != "source" && a.Mutation != "tests" {
		return fmt.Errorf("агент %s: require_diff требует mutation source или tests", dirName)
	}
	if (a.Mutation == "source" || a.Mutation == "tests") && len(a.AllowedPaths) == 0 {
		return fmt.Errorf("агент %s: mutation %s требует allowed_paths", dirName, a.Mutation)
	}
	if a.Mutation != "source" && a.Mutation != "tests" && len(a.AllowedPaths) > 0 {
		return fmt.Errorf("агент %s: allowed_paths разрешён только для mutation source или tests", dirName)
	}
	for _, pattern := range a.AllowedPaths {
		if err := scope.Validate(pattern); err != nil {
			return fmt.Errorf("агент %s: %w", dirName, err)
		}
		for _, segment := range strings.Split(strings.ReplaceAll(pattern, "\\", "/"), "/") {
			if strings.HasPrefix(segment, ".git") || strings.HasPrefix(segment, ".ai-team") {
				return fmt.Errorf("агент %s: allowed_paths не может разрешать reserved controller segment %q", dirName, segment)
			}
		}
	}
	checkNames := make(map[string]bool, len(a.Checks))
	for _, check := range a.Checks {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("агент %s: %w", dirName, err)
		}
		if checkNames[check.Name] {
			return fmt.Errorf("агент %s: check %q дублируется", dirName, check.Name)
		}
		checkNames[check.Name] = true
	}
	for inputName, artifactPath := range a.Inputs {
		if err := validateArtifactPath(artifactPath); err != nil {
			return fmt.Errorf("агент %s: input %s: %w", dirName, inputName, err)
		}
	}
	hasMarkdownOutput := false
	for outputName, artifactPath := range a.Outputs {
		if err := validateArtifactPath(artifactPath); err != nil {
			return fmt.Errorf("агент %s: output %s: %w", dirName, outputName, err)
		}
		hasMarkdownOutput = hasMarkdownOutput || strings.EqualFold(path.Ext(artifactPath), ".md")
	}
	for inputName, inputPath := range a.Inputs {
		for outputName, outputPath := range a.Outputs {
			if artifactPathsOverlap(inputPath, outputPath) {
				return fmt.Errorf("агент %s: input %s и output %s пересекаются (%s, %s)", dirName, inputName, outputName, inputPath, outputPath)
			}
		}
	}
	if a.Verdict != nil && !hasMarkdownOutput {
		return fmt.Errorf("агент %s: verdict contract требует хотя бы один markdown output", dirName)
	}
	if a.Kind == "delivery" && len(a.Preconditions) == 0 {
		return fmt.Errorf("агент %s: kind delivery требует declarative preconditions", dirName)
	}
	if a.Kind == "delivery" {
		planPath, exists := a.Outputs["plan"]
		if !exists || !strings.EqualFold(path.Ext(planPath), ".json") {
			return fmt.Errorf("агент %s: kind delivery требует JSON output с именем plan", dirName)
		}
		if len(a.Outputs) != 1 {
			return fmt.Errorf("агент %s: kind delivery разрешает только output plan", dirName)
		}
		if a.Verdict != nil || len(a.Checks) > 0 {
			return fmt.Errorf("агент %s: delivery verdict/checks должны быть controller-owned до этапа", dirName)
		}
	}
	for inputName, contract := range a.Preconditions {
		if _, exists := a.Inputs[inputName]; !exists {
			return fmt.Errorf("агент %s: precondition ссылается на неизвестный input %s", dirName, inputName)
		}
		if contract == nil {
			return fmt.Errorf("агент %s: precondition %s не может быть пустым", dirName, inputName)
		}
		if err := contract.Validate(); err != nil {
			return fmt.Errorf("агент %s: precondition %s: %w", dirName, inputName, err)
		}
		if a.Kind == "delivery" {
			for _, value := range contract.Values {
				if value.IsNegative() {
					return fmt.Errorf("агент %s: delivery precondition %s не может разрешать негативный verdict %s", dirName, inputName, value)
				}
			}
		}
	}
	if a.Kind == "delivery" {
		if a.RuntimeType != "delivery" || a.CLI != "" || a.PromptFile != "" {
			return fmt.Errorf("агент %s: delivery требует runtime delivery без cli/prompt_file", dirName)
		}
	} else {
		if a.RuntimeType != "agentcli" {
			return fmt.Errorf("агент %s: поддерживается только runtime agentcli; %q недоступен", dirName, a.RuntimeType)
		}
		if a.CLI != "" && a.CLI != "opencode" {
			return fmt.Errorf("агент %s: поддерживается только cli opencode", dirName)
		}
		if a.PromptFile == "" {
			return fmt.Errorf("агент %s: prompt_file обязателен для agent runtime", dirName)
		}
	}
	return nil
}

func validateArtifactPath(value string) error {
	normalized := strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(normalized)
	if value == "" || path.IsAbs(normalized) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("путь %q должен быть относительным и оставаться внутри artifact root", value)
	}
	if normalized != cleaned {
		return fmt.Errorf("путь %q должен быть каноническим (ожидался %q)", value, cleaned)
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if strings.ContainsAny(segment, "{}") && segment != "{feature}" {
			return fmt.Errorf("путь %q содержит неизвестную template-переменную", value)
		}
	}
	return nil
}

func artifactPathsOverlap(first, second string) bool {
	first = path.Clean(strings.ReplaceAll(first, "\\", "/"))
	second = path.Clean(strings.ReplaceAll(second, "\\", "/"))
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

// Exists сообщает, доступен ли агент (для валидации конфига).
func (r *Registry) Exists(name string) bool {
	_, err := r.Load(name)
	return err == nil
}

// LoadFailure сообщает, что каталог агента обнаружен в одном из registry
// layers, но не смог быть загружен целиком (невалидный def.yaml, нечитаемый
// prompt_file и т.д.).
type LoadFailure struct {
	Name string
	Err  error
}

// List перечисляет все обнаруживаемые агенты across layers. Каталог, который
// не смог загрузиться, не пропускается молча — он возвращается в failures,
// чтобы вызывающий код (например, `ai-team list`) мог сообщить об этом, а не
// показать пустое место без следа ошибки.
func (r *Registry) List() (agents []*Agent, failures []LoadFailure) {
	names := make(map[string]bool)
	for _, layer := range r.layers {
		entries, err := fs.ReadDir(layer.FS, ".")
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				names[entry.Name()] = true
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		a, err := r.Load(name)
		if err != nil {
			failures = append(failures, LoadFailure{Name: name, Err: err})
			continue
		}
		agents = append(agents, a)
	}
	return agents, failures
}

func (r *Registry) DefaultPipeline() []string {
	return []string{"analyst", "architect", "coder", "reviewer", "tester", "verifier", "deployer"}
}

// ValidatePipeline compiles the artifact ownership graph before any stage is
// executed. A path can have exactly one producer, and an output cannot own a
// parent/child subtree already owned by another stage.
func (r *Registry) ValidatePipeline(names []string) error {
	type owner struct {
		stage string
		name  string
		path  string
	}
	var outputs []owner
	for _, stage := range names {
		a, err := r.Load(stage)
		if err != nil {
			return err
		}
		for outputName, outputPath := range a.Outputs {
			cleaned := path.Clean(strings.ReplaceAll(outputPath, "\\", "/"))
			if reservedArtifactOutput(cleaned) {
				return fmt.Errorf("агент %s: output %s использует controller-owned namespace %q", stage, outputName, outputPath)
			}
			for _, previous := range outputs {
				if artifactPathsOverlap(previous.path, cleaned) {
					return fmt.Errorf("artifact graph: output %s.%s (%s) пересекается с %s.%s (%s)",
						stage, outputName, cleaned, previous.stage, previous.name, previous.path)
				}
			}
			outputs = append(outputs, owner{stage: stage, name: outputName, path: cleaned})
		}
	}
	return nil
}

func reservedArtifactOutput(value string) bool {
	segments := strings.Split(value, "/")
	if len(segments) == 0 {
		return true
	}
	if segments[0] == "tasks" {
		return true
	}
	return len(segments) > 1 && segments[0] == "{feature}" &&
		(segments[1] == "status" || segments[1] == ".stage-summary" || segments[1] == ".control")
}
