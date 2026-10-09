package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	return ParseYAML(data)
}

// ParseYAML strictly decodes one project config from bytes, using the same
// field and document checks as Load. Web validation uses this before publish.
func ParseYAML(data []byte) (*Config, error) {
	if len(data) == 0 || len(data) > MaxTemplateYAMLBytes {
		return nil, fmt.Errorf("config YAML должен занимать от 1 до %d bytes", MaxTemplateYAMLBytes)
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("config: multiple YAML documents are not supported")
		}
		return nil, err
	}
	return &cfg, nil
}

// defaultLoopbackSources — стадии, чей негативный вердикт возвращается в работу.
var defaultLoopbackSources = []string{"reviewer", "tester", "verifier"}

func hasStage(index map[string]int, name string) bool { _, ok := index[name]; return ok }

// defaultStageRoles — human-роли рёбер default workflow стандартного профиля.
var defaultStageRoles = map[string][]string{
	"analyst":   {"product_owner"},
	"architect": {"architect"},
	"coder":     {"developer"},
	"reviewer":  {"reviewer"},
	"tester":    {"qa"},
	"verifier":  {"qa"},
}

// Profile — предустановленный вариант единственного project template, который
// init разворачивает в обычный schema v5 конфиг.
const (
	ProfileStandard  = "standard"
	ProfileFast      = "fast"
	ProfileRegulated = "regulated"
)

type stageSpec struct {
	name              string
	roles             []string
	maxVisits         int
	askQuestions      bool
	productSpecOutput bool
}

var defaultTemplateReturns = []TemplateReturn{
	{From: "tech_design", To: "product_spec"},
	{From: "design_review", To: "tech_design"},
	{From: "code_review", To: "implementation"},
	{From: "qa", To: "implementation"},
	{From: "qa", To: "product_spec"},
	{From: "observation", To: "implementation"},
	{From: "acceptance", To: "product_spec"},
}

func ideaToProdStages() []TemplateStage {
	return []TemplateStage{
		{ID: "intent", Title: "Бизнес-идея", Function: "bo", Result: "approve", Executor: "human"},
		{ID: "product_spec", Title: "Продуктовая спецификация", Function: "po", Result: "md", Executor: "human", Agent: "analyst", RequiredSections: []string{"Критерии приёмки"}, Check: &TemplateCheck{Kind: "agent", Agent: "verifier", Mode: "grill", MaxRounds: 3}},
		{ID: "tech_design", Title: "Техническое решение", Function: "architect", Result: "md", Executor: "agent", Agent: "architect", Check: &TemplateCheck{Kind: "agent", Agent: "verifier"}, Confirm: "required", Skippable: true},
		{ID: "design_review", Title: "Ревью техрешения", Function: "reviewer", Result: "approve", Executor: "human", Agent: "design-reviewer", Skippable: true},
		{ID: "implementation", Title: "Реализация", Function: "developer", Result: "link", LinkKind: "pr", Executor: "human", Agent: "coder", Delivery: &TemplateDelivery{}, Check: &TemplateCheck{Kind: "hard", Rules: []string{"pr_exists", "pr_open", "pr_base_branch"}}},
		{ID: "code_review", Title: "Код-ревью", Function: "reviewer", Result: "md", Executor: "human", Agent: "reviewer", Check: &TemplateCheck{Kind: "hard", Rules: []string{"verdict_marker"}}},
		{ID: "qa", Title: "Тестирование", Function: "qa", Result: "md", Executor: "human", Agent: "tester"},
		{ID: "deploy", Title: "Выкладка", Function: "deployer", Result: "link", LinkKind: "build", Executor: "human"},
		{ID: "observation", Title: "Наблюдение после выкладки", Function: "deployer", Result: "md", Executor: "human", Agent: "observer"},
		{ID: "acceptance", Title: "Окончательная готовность", Function: "bo", Result: "approve", Executor: "human"},
	}
}

// DefaultProfile builds one v5 process template from an init preset. The
// standard preset is the built-in idea-to-prod template; fast and regulated
// materialize variants as distinct template names rather than runtime modes.
func DefaultProfile(profile string) (*Config, error) {
	stages := ideaToProdStages()
	templateName := "idea-to-prod"
	title := "От идеи до продакшена"
	maxVisits := 3
	switch profile {
	case ProfileStandard:
	case ProfileFast:
		templateName = "idea-to-prod-fast"
		title = "Быстрый поток от идеи до продакшена"
		maxVisits = 2
	case ProfileRegulated:
		templateName = "idea-to-prod-regulated"
		title = "Регламентированный поток от идеи до продакшена"
		maxVisits = 2
	default:
		return nil, fmt.Errorf("неизвестный профиль %q (допустимы %s, %s, %s)",
			profile, ProfileFast, ProfileStandard, ProfileRegulated)
	}
	if profile == ProfileFast {
		for index := range stages {
			stages[index].Confirm = "auto"
		}
	}
	if profile == ProfileRegulated {
		for index := range stages {
			stages[index].Confirm = "required"
		}
	}
	for index := range stages {
		if stages[index].Confirm == "" {
			if stages[index].Result == "approve" {
				stages[index].Confirm = "auto"
			} else {
				stages[index].Confirm = "required"
			}
		}
	}
	maxVisitsByTarget := map[string]int{}
	for _, route := range defaultTemplateReturns {
		maxVisitsByTarget[route.To] = maxVisits
	}
	cfg := &Config{
		SchemaVersion: CurrentSchemaVersion,
		Template:      templateName,
		Title:         title,
		StallAfter:    "24h",
		Stages:        stages,
		Returns:       append([]TemplateReturn(nil), defaultTemplateReturns...),
		MaxVisits:     maxVisitsByTarget,
		CLI:           "opencode",
		Effort:        "medium",
		StageTimeout:  "30m",
	}
	for _, stage := range stages {
		cfg.PipelineAgents = append(cfg.PipelineAgents, AgentConfig{Name: stage.ID})
	}
	return cfg, nil
}

func QuorumAny() string { return "any" }
func QuorumAll() string { return "all" }

func buildConfig(profile string, stages []stageSpec, quorum, loopbackQuorum string, maxVisits int) *Config {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	agents := make([]AgentConfig, len(names))
	for i, name := range names {
		agents[i] = AgentConfig{Name: name}
	}
	cfg := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: agents,
		CLI:            "opencode",
		Effort:         "medium",
		StageTimeout:   "30m",
	}
	cfg.Workflow = buildWorkflow(profile, names, stages, quorum, loopbackQuorum, maxVisits)
	return cfg
}

func buildWorkflow(profile string, names []string, stages []stageSpec, quorum, loopbackQuorum string, defaultMaxVisits int) *WorkflowConfig {
	index := make(map[string]int, len(names))
	for i, name := range names {
		index[name] = i
	}
	specByName := make(map[string]stageSpec, len(stages))
	for _, s := range stages {
		specByName[s.name] = s
	}
	workflowConfig := &WorkflowConfig{Entry: names[0], MaxVisits: map[string]int{}}
	// APF-1: в standard/fast профилях forward gate-переходы откладываются и
	// подтверждаются одним consolidated delivery-решением (1–2 клика на фичу);
	// regulated сохраняет пошаговые approvals (quorum all).
	deferredGates := profile == ProfileStandard || profile == ProfileFast
	for i, name := range names {
		target := "$complete"
		if i+1 < len(names) {
			target = names[i+1]
		}
		edge := WorkflowEdgeConfig{From: name, Outcome: "passed", To: target}
		if target != "$complete" {
			roles := append([]string(nil), specByName[name].roles...)
			if len(roles) == 0 {
				roles = []string{"operator"}
			}
			edge.Approval = &WorkflowApprovalConfig{
				Roles: roles, Quorum: quorum, Deferred: deferredGates,
				Actions: map[string]string{"approve": target, "reject": "$stop"},
			}
			if specByName[name].productSpecOutput {
				// Product discovery must be explicitly agreed before technical
				// planning starts, even in profiles that consolidate later gates.
				edge.Approval.Deferred = false
				edge.Approval.Actions = map[string]string{"approve_spec": target, "reject": "$stop"}
			}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, edge)
	}
	for _, name := range names {
		stage := specByName[name]
		if !stage.askQuestions {
			continue
		}
		roles := append([]string(nil), stage.roles...)
		if len(roles) == 0 {
			roles = []string{"operator"}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, WorkflowEdgeConfig{
			From: name, Outcome: "blocked", To: name,
			Approval: &WorkflowApprovalConfig{
				Roles: roles, Quorum: quorum,
				Actions: map[string]string{"answer_questions": name, "stop": "$stop"},
			},
		})
	}
	if architectIdx, exists := index["architect"]; exists && hasStage(index, "analyst") && index["analyst"] < architectIdx {
		roles := append([]string(nil), specByName["architect"].roles...)
		if len(roles) == 0 {
			roles = []string{"operator"}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, WorkflowEdgeConfig{
			From: "architect", Outcome: "rejected", To: "analyst",
			Approval: &WorkflowApprovalConfig{Roles: roles, Quorum: loopbackQuorum,
				Actions: map[string]string{"return_to_analyst": "analyst", "stop": "$stop"}},
		})
	}
	coderIdx, hasCoder := index["coder"]
	for _, source := range defaultLoopbackSources {
		srcIdx, srcExists := index[source]
		if !hasCoder || !srcExists || srcIdx <= coderIdx {
			continue
		}
		override := "$complete"
		if srcIdx+1 < len(names) {
			override = names[srcIdx+1]
		}
		roles := append([]string(nil), specByName[source].roles...)
		if len(roles) == 0 {
			roles = []string{"reviewer"}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, WorkflowEdgeConfig{
			From: source, Outcome: "rejected", To: "coder",
			Approval: &WorkflowApprovalConfig{
				Roles: roles, Quorum: loopbackQuorum,
				Actions: loopbackActions(index, override),
			},
		})
	}
	for _, name := range names {
		if specByName[name].askQuestions {
			// Question-enabled stages may ask up to three clarification rounds
			// and still report the final unresolved question as blocked.
			workflowConfig.MaxVisits[name] = 4
		} else if name == "architect" {
			workflowConfig.MaxVisits[name] = 3
		} else if mv := specByName[name].maxVisits; mv > 0 {
			workflowConfig.MaxVisits[name] = mv
		} else if name != "deployer" && name != "architect" {
			workflowConfig.MaxVisits[name] = defaultMaxVisits
		}
	}
	return workflowConfig
}

func loopbackActions(index map[string]int, override string) map[string]string {
	actions := map[string]string{"return_to_coder": "coder", "override_approve": override, "reject": "$stop"}
	if hasStage(index, "architect") {
		actions["return_to_architect"] = "architect"
	}
	return actions
}

// Default returns the legacy in-memory standard graph for current Go runtime
// callers. `init` uses DefaultProfile and always writes schema v5.
func Default() *Config {
	stages := []stageSpec{
		{name: "analyst", roles: []string{"product_owner"}, askQuestions: true, productSpecOutput: true},
		{name: "architect", roles: []string{"architect"}},
		{name: "coder", roles: []string{"developer"}, maxVisits: 3},
		{name: "reviewer", roles: []string{"reviewer"}, maxVisits: 3},
		{name: "tester", roles: []string{"qa"}, maxVisits: 3},
		{name: "verifier", roles: []string{"qa"}, maxVisits: 3},
		{name: "deployer"},
	}
	return buildConfig(ProfileStandard, stages, QuorumAny(), QuorumAny(), 3)
}

// Marshal serializes the project template and supported configuration fields.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}
