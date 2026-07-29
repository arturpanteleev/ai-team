package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

// Допустимые значения полей (валидируются в Validate).
const (
	CurrentSchemaVersion  = 4
	PreviousSchemaVersion = 3
	SchemaVersion2        = 2
	LegacySchemaVersion   = 1

	TransitionAuto      = "auto"
	TransitionByConfirm = "by_confirm"
	TransitionGate      = "gate"

	CheckpointAuto            = "auto_continue"
	CheckpointInteractive     = "interactive"
	CheckpointRequireExplicit = "require_explicit"

	OnNegativeStop     = "stop"
	OnNegativeAsk      = "ask"
	OnNegativeContinue = "continue"
)

type AgentConfig struct {
	Name              string              `yaml:"name"`
	Model             string              `yaml:"model,omitempty"`
	Effort            string              `yaml:"effort,omitempty"`
	CLI               string              `yaml:"cli,omitempty"`
	Transition        string              `yaml:"transition,omitempty"`
	MaxRetries        int                 `yaml:"max_retries,omitempty"`
	GateAfter         bool                `yaml:"gate_after,omitempty"`
	GateBefore        bool                `yaml:"gate_before,omitempty"`
	Timeout           string              `yaml:"timeout,omitempty"`
	OnNegativeVerdict string              `yaml:"on_negative_verdict,omitempty"`
	LoopbackTo        string              `yaml:"loopback_to,omitempty"`
	CheckpointAfter   string              `yaml:"checkpoint_after,omitempty"`
	CheckpointBefore  string              `yaml:"checkpoint_before,omitempty"`
	ApprovalRoles     []string            `yaml:"approval_roles,omitempty"`
	ApprovalQuorum    string              `yaml:"approval_quorum,omitempty"`
	Checks            []checks.Definition `yaml:"checks,omitempty"`
}

type Config struct {
	SchemaVersion  int             `yaml:"schema_version,omitempty"`
	PipelineAgents []AgentConfig   `yaml:"pipeline"`
	Workflow       *WorkflowConfig `yaml:"workflow,omitempty"`
	CLI            string          `yaml:"cli,omitempty"`
	Model          string          `yaml:"model,omitempty"`
	Effort         string          `yaml:"effort,omitempty"`
	StageTimeout   string          `yaml:"stage_timeout,omitempty"`
}

type WorkflowConfig struct {
	Entry     string               `yaml:"entry"`
	MaxVisits map[string]int       `yaml:"max_visits,omitempty"`
	Edges     []WorkflowEdgeConfig `yaml:"edges"`
}

type WorkflowEdgeConfig struct {
	From     string                  `yaml:"from"`
	Outcome  string                  `yaml:"outcome"`
	To       string                  `yaml:"to"`
	Approval *WorkflowApprovalConfig `yaml:"approval,omitempty"`
}

type WorkflowApprovalConfig struct {
	Roles   []string          `yaml:"roles"`
	Quorum  string            `yaml:"quorum"`
	Actions map[string]string `yaml:"actions"`
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"schema_version": true, "pipeline": true, "cli": true, "model": true,
		"effort": true, "stage_timeout": true, "workflow": true,
	}, "config"); err != nil {
		return err
	}
	type rawConfig struct {
		SchemaVersion int             `yaml:"schema_version"`
		Pipeline      yaml.Node       `yaml:"pipeline"`
		CLI           string          `yaml:"cli"`
		Model         string          `yaml:"model"`
		Effort        string          `yaml:"effort"`
		StageTimeout  string          `yaml:"stage_timeout"`
		Workflow      *WorkflowConfig `yaml:"workflow"`
	}
	var raw rawConfig
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.SchemaVersion = raw.SchemaVersion
	if c.SchemaVersion == 0 {
		c.SchemaVersion = LegacySchemaVersion
	}
	c.CLI = raw.CLI
	c.Model = raw.Model
	c.Effort = raw.Effort
	c.StageTimeout = raw.StageTimeout
	c.Workflow = raw.Workflow

	if raw.Pipeline.Kind == 0 {
		return fmt.Errorf("config: pipeline is required")
	}
	if raw.Pipeline.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: pipeline must be a list")
	}
	allowedAgentFields := map[string]bool{
		"name": true, "model": true, "effort": true, "cli": true,
		"transition": true, "max_retries": true, "gate_after": true,
		"gate_before": true, "timeout": true, "on_negative_verdict": true,
		"loopback_to":      true,
		"checkpoint_after": true, "checkpoint_before": true,
		"approval_roles": true, "approval_quorum": true,
		"checks": true,
	}
	for i, item := range raw.Pipeline.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			if item.Tag != "!!str" || item.Value == "" {
				return fmt.Errorf("config: pipeline[%d] invalid scalar", i)
			}
			c.PipelineAgents = append(c.PipelineAgents, AgentConfig{Name: item.Value})
		case yaml.MappingNode:
			if err := validateMappingKeys(item, allowedAgentFields, fmt.Sprintf("config: pipeline[%d]", i)); err != nil {
				return err
			}
			var ac AgentConfig
			if err := item.Decode(&ac); err != nil {
				return fmt.Errorf("config: pipeline[%d] unmarshal: %w", i, err)
			}
			if ac.Name == "" {
				return fmt.Errorf("config: pipeline[%d] missing 'name'", i)
			}
			c.PipelineAgents = append(c.PipelineAgents, ac)
		default:
			return fmt.Errorf("config: pipeline[%d] invalid type", i)
		}
	}

	return nil
}

func (w *WorkflowConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"entry": true, "max_visits": true, "edges": true,
	}, "config: workflow"); err != nil {
		return err
	}
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "max_visits" {
			if err := rejectDuplicateMappingKeys(value.Content[index+1], "config: workflow max_visits"); err != nil {
				return err
			}
		}
	}
	type plain WorkflowConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*w = WorkflowConfig(decoded)
	return nil
}

func (e *WorkflowEdgeConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"from": true, "outcome": true, "to": true, "approval": true,
	}, "config: workflow edge"); err != nil {
		return err
	}
	type plain WorkflowEdgeConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*e = WorkflowEdgeConfig(decoded)
	return nil
}

func (a *WorkflowApprovalConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"roles": true, "quorum": true, "actions": true,
	}, "config: workflow approval"); err != nil {
		return err
	}
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "actions" {
			if err := rejectDuplicateMappingKeys(value.Content[index+1], "config: workflow approval actions"); err != nil {
				return err
			}
		}
	}
	type plain WorkflowApprovalConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*a = WorkflowApprovalConfig(decoded)
	return nil
}

func rejectDuplicateMappingKeys(node *yaml.Node, context string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected mapping", context)
	}
	seen := map[string]bool{}
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", context, key)
		}
		seen[key] = true
	}
	return nil
}

func validateMappingKeys(node *yaml.Node, allowed map[string]bool, context string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected mapping", context)
	}
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", context, key)
		}
		seen[key] = true
		if !allowed[key] {
			return fmt.Errorf("%s: unknown field %q", context, key)
		}
	}
	return nil
}

func (c *Config) AgentNames() []string {
	names := make([]string, len(c.PipelineAgents))
	for i, a := range c.PipelineAgents {
		names[i] = a.Name
	}
	return names
}

// CompiledGraph нормализует schema v4 и legacy linear pipeline в единый
// immutable runtime contract.
func (c *Config) CompiledGraph() (workflow.Graph, error) {
	graph := workflow.Graph{SchemaVersion: 1}
	for _, stage := range c.PipelineAgents {
		graph.Nodes = append(graph.Nodes, workflow.Node{Name: stage.Name})
	}
	if len(graph.Nodes) == 0 {
		return graph, fmt.Errorf("workflow graph: pipeline пуст")
	}
	graph.Entry = graph.Nodes[0].Name
	if c.SchemaVersion == CurrentSchemaVersion {
		graph.SchemaVersion = CurrentSchemaVersion
		if c.Workflow == nil {
			return graph, fmt.Errorf("schema_version %d требует workflow", CurrentSchemaVersion)
		}
		graph.Entry = c.Workflow.Entry
		knownNodes := make(map[string]bool, len(graph.Nodes))
		for _, node := range graph.Nodes {
			knownNodes[node.Name] = true
		}
		for name := range c.Workflow.MaxVisits {
			if !knownNodes[name] {
				return graph, fmt.Errorf("workflow graph: max_visits ссылается на неизвестный node %q", name)
			}
		}
		for index := range graph.Nodes {
			graph.Nodes[index].MaxVisits = c.Workflow.MaxVisits[graph.Nodes[index].Name]
		}
		for _, edge := range c.Workflow.Edges {
			compiled := workflow.Edge{
				From: edge.From, Outcome: workflow.Outcome(edge.Outcome), To: edge.To,
			}
			if edge.Approval != nil {
				compiled.Approval = &workflow.ApprovalPolicy{
					Roles:   append([]string(nil), edge.Approval.Roles...),
					Quorum:  edge.Approval.Quorum,
					Actions: copyTargets(edge.Approval.Actions),
				}
			}
			graph.Edges = append(graph.Edges, compiled)
		}
		if err := graph.Validate(true, true); err != nil {
			return graph, err
		}
		return graph, nil
	}
	for index, stage := range c.PipelineAgents {
		target := workflow.TerminalComplete
		if index+1 < len(c.PipelineAgents) {
			target = c.PipelineAgents[index+1].Name
		}
		edge := workflow.Edge{From: stage.Name, Outcome: workflow.OutcomePassed, To: target}
		if !workflow.IsTerminal(target) && len(stage.ApprovalRoles) > 0 {
			quorum := stage.ApprovalQuorum
			if quorum == "" {
				quorum = "any"
			}
			edge.Approval = &workflow.ApprovalPolicy{
				Roles: append([]string(nil), stage.ApprovalRoles...), Quorum: quorum,
				Actions: map[string]string{"approve": target, "reject": workflow.TerminalStop},
			}
		}
		graph.Edges = append(graph.Edges, edge)
		if stage.LoopbackTo != "" {
			graph.Edges = append(graph.Edges, workflow.Edge{
				From: stage.Name, Outcome: workflow.OutcomeRejected, To: stage.LoopbackTo,
			})
		}
	}
	if err := graph.Validate(false, false); err != nil {
		return graph, err
	}
	return graph, nil
}

func copyTargets(source map[string]string) map[string]string {
	targets := make(map[string]string, len(source))
	for action, target := range source {
		targets[action] = target
	}
	return targets
}

// AgentConfig возвращает конфигурацию агента с подставленными глобальными
// значениями и дефолтами.
func (c *Config) AgentConfig(name string) *AgentConfig {
	for _, a := range c.PipelineAgents {
		if a.Name == name {
			cfg := a
			if cfg.Model == "" {
				cfg.Model = c.Model
			}
			if cfg.Effort == "" {
				cfg.Effort = c.Effort
			}
			if cfg.CLI == "" {
				cfg.CLI = c.CLI
			}
			if cfg.Transition == "" {
				cfg.Transition = TransitionAuto
			}
			if cfg.Timeout == "" {
				cfg.Timeout = c.StageTimeout
			}
			if cfg.OnNegativeVerdict == "" {
				cfg.OnNegativeVerdict = OnNegativeStop
			}
			if len(cfg.ApprovalRoles) > 0 && cfg.ApprovalQuorum == "" {
				cfg.ApprovalQuorum = "any"
			}
			return &cfg
		}
	}
	return nil
}

// StageTimeoutFor возвращает распарсенный таймаут этапа (0 — без таймаута).
func (ac *AgentConfig) StageTimeoutFor() (time.Duration, error) {
	if ac.Timeout == "" {
		return 0, nil
	}
	return time.ParseDuration(ac.Timeout)
}

func (ac *AgentConfig) CheckpointAfterPolicy() string {
	if ac.CheckpointAfter != "" {
		return ac.CheckpointAfter
	}
	if ac.GateAfter || ac.Transition == TransitionGate {
		return CheckpointRequireExplicit
	}
	if ac.Transition == TransitionByConfirm {
		return CheckpointInteractive
	}
	return CheckpointAuto
}

func (ac *AgentConfig) CheckpointBeforePolicy() string {
	if ac.CheckpointBefore != "" {
		return ac.CheckpointBefore
	}
	if ac.GateBefore {
		return CheckpointRequireExplicit
	}
	return CheckpointAuto
}

// AgentLookup отвечает, существует ли агент (реализуется registry).
type AgentLookup interface {
	Exists(name string) bool
}

type pipelineValidator interface {
	ValidatePipeline(names []string) error
}

// Validate проверяет конфиг до запуска пайплайна (fail fast).
func (c *Config) Validate(reg AgentLookup) error {
	if len(c.PipelineAgents) == 0 {
		return fmt.Errorf("config: pipeline пуст")
	}
	var errs []string
	validate := func(cond bool, format string, args ...interface{}) {
		if !cond {
			errs = append(errs, fmt.Sprintf(format, args...))
		}
	}
	validate(c.SchemaVersion == 0 || c.SchemaVersion == LegacySchemaVersion || c.SchemaVersion == SchemaVersion2 ||
		c.SchemaVersion == PreviousSchemaVersion || c.SchemaVersion == CurrentSchemaVersion,
		"schema_version %d не поддерживается (поддерживаются %d, %d, %d и %d)",
		c.SchemaVersion, LegacySchemaVersion, SchemaVersion2, PreviousSchemaVersion, CurrentSchemaVersion)
	validate(c.SchemaVersion == CurrentSchemaVersion || c.Workflow == nil,
		"workflow разрешён только в schema_version %d", CurrentSchemaVersion)

	if c.StageTimeout != "" {
		if duration, err := time.ParseDuration(c.StageTimeout); err != nil || duration <= 0 {
			errs = append(errs, fmt.Sprintf("stage_timeout %q не парсится (пример: 30m)", c.StageTimeout))
		}
	}
	validate(isOneOf(c.Effort, "", "low", "medium", "high"),
		"effort %q недопустим (low|medium|high)", c.Effort)
	validate(isOneOf(c.CLI, "", "opencode"),
		"cli %q не поддерживается (реализован только явный adapter opencode)", c.CLI)

	seenNames := make(map[string]bool, len(c.PipelineAgents))
	for _, a := range c.PipelineAgents {
		validate(a.Name != "", "имя агента обязательно")
		validate(!seenNames[a.Name], "агент %q повторяется в pipeline", a.Name)
		if a.LoopbackTo != "" {
			validate(seenNames[a.LoopbackTo], "%s: loopback_to %q должен точно ссылаться на предыдущий этап", a.Name, a.LoopbackTo)
		}
		seenNames[a.Name] = true
		if reg != nil {
			validate(reg.Exists(a.Name), "агент %q не найден в registry", a.Name)
		}
		validate(isOneOf(a.Transition, "", TransitionAuto, TransitionByConfirm, TransitionGate),
			"%s: transition %q недопустим (auto|by_confirm|gate)", a.Name, a.Transition)
		validate(isOneOf(a.Effort, "", "low", "medium", "high"),
			"%s: effort %q недопустим (low|medium|high)", a.Name, a.Effort)
		validate(isOneOf(a.CLI, "", "opencode"),
			"%s: cli %q не поддерживается (реализован только явный adapter opencode)", a.Name, a.CLI)
		validate(isOneOf(a.OnNegativeVerdict, "", OnNegativeStop, OnNegativeAsk, OnNegativeContinue),
			"%s: on_negative_verdict %q недопустим (stop|ask|continue)", a.Name, a.OnNegativeVerdict)
		validate(a.MaxRetries >= 0, "%s: max_retries не может быть отрицательным", a.Name)
		validate(isOneOf(a.CheckpointAfter, "", CheckpointAuto, CheckpointInteractive, CheckpointRequireExplicit),
			"%s: checkpoint_after %q недопустим", a.Name, a.CheckpointAfter)
		validate(isOneOf(a.CheckpointBefore, "", CheckpointAuto, CheckpointInteractive, CheckpointRequireExplicit),
			"%s: checkpoint_before %q недопустим", a.Name, a.CheckpointBefore)
		validate(a.CheckpointAfter == "" || !a.GateAfter && a.Transition == "",
			"%s: checkpoint_after нельзя совмещать с legacy gate_after/transition", a.Name)
		validate(a.CheckpointBefore == "" || !a.GateBefore,
			"%s: checkpoint_before нельзя совмещать с legacy gate_before", a.Name)
		roleNames := make(map[string]bool, len(a.ApprovalRoles))
		for _, role := range a.ApprovalRoles {
			validate(strings.TrimSpace(role) != "", "%s: approval_roles не может содержать пустую роль", a.Name)
			validate(!roleNames[role], "%s: approval role %q повторяется", a.Name, role)
			roleNames[role] = true
		}
		validate(isOneOf(a.ApprovalQuorum, "", "any", "all"),
			"%s: approval_quorum %q недопустим (any|all)", a.Name, a.ApprovalQuorum)
		validate(a.ApprovalQuorum == "" || len(a.ApprovalRoles) > 0,
			"%s: approval_quorum требует approval_roles", a.Name)
		if c.SchemaVersion == PreviousSchemaVersion || c.SchemaVersion == CurrentSchemaVersion {
			validate(!a.GateAfter && !a.GateBefore && a.Transition == "",
				"%s: schema_version %d запрещает legacy gate_after/gate_before/transition", a.Name, c.SchemaVersion)
		}
		if c.SchemaVersion == CurrentSchemaVersion {
			validate(a.LoopbackTo == "" && a.OnNegativeVerdict == "" && a.MaxRetries == 0 &&
				len(a.ApprovalRoles) == 0 && a.ApprovalQuorum == "",
				"%s: schema_version %d хранит route, retries и approvals только в workflow edges/max_visits",
				a.Name, CurrentSchemaVersion)
		}
		if a.Timeout != "" {
			if duration, err := time.ParseDuration(a.Timeout); err != nil || duration <= 0 {
				errs = append(errs, fmt.Sprintf("%s: timeout %q не парсится (пример: 45m)", a.Name, a.Timeout))
			}
		}
		checkNames := make(map[string]bool, len(a.Checks))
		for _, check := range a.Checks {
			if err := check.Validate(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", a.Name, err))
			}
			validate(!checkNames[check.Name], "%s: check %q повторяется", a.Name, check.Name)
			checkNames[check.Name] = true
		}
	}
	if validator, ok := reg.(pipelineValidator); ok {
		if err := validator.ValidatePipeline(c.AgentNames()); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if _, err := c.CompiledGraph(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("невалидный config.yaml:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func isOneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
