package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

// Допустимые значения полей (валидируются в Validate).
const (
	// CurrentSchemaVersion — единственная поддерживаемая схема. Легаси
	// схемы 1–3 удалены: маршрут, retries и approvals живут только в
	// workflow edges/max_visits.
	CurrentSchemaVersion = 4
)

type AgentConfig struct {
	Name    string              `yaml:"name"`
	Model   string              `yaml:"model,omitempty"`
	Effort  string              `yaml:"effort,omitempty"`
	CLI     string              `yaml:"cli,omitempty"`
	Timeout string              `yaml:"timeout,omitempty"`
	Checks  []checks.Definition `yaml:"checks,omitempty"`
}

type Config struct {
	SchemaVersion  int                `yaml:"schema_version,omitempty"`
	PipelineAgents []AgentConfig      `yaml:"pipeline"`
	Workflow       *WorkflowConfig    `yaml:"workflow,omitempty"`
	CLI            string             `yaml:"cli,omitempty"`
	Model          string             `yaml:"model,omitempty"`
	Effort         string             `yaml:"effort,omitempty"`
	StageTimeout   string             `yaml:"stage_timeout,omitempty"`
	Containment    *ContainmentConfig `yaml:"containment,omitempty"`
	TreeHash       *TreeHashConfig    `yaml:"tree_hash,omitempty"`
	Budget         *BudgetConfig      `yaml:"budget,omitempty"`
}

// BudgetConfig — глобальные жёсткие лимиты run (P1-7): total wall-time и
// суммарное число попыток. Лимиты применяются ВСЕГДА (даже без секции —
// канонические дефолты), это контракт: run не может превысить wall-time или
// attempts бюджета.
type BudgetConfig struct {
	MaxWallTime string `yaml:"max_wall_time,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty"`
}

// Канонические дефолты бюджета (применяются при отсутствии явного budget).
const (
	DefaultBudgetMaxWallTime = "24h"
	DefaultBudgetMaxAttempts = 100
)

// EffectiveMaxWallTime возвращает wall-time лимит: явный или канонический
// дефолт. Значение валидно (Validate вызывается до запуска).
func (bc *BudgetConfig) EffectiveMaxWallTime() (time.Duration, string) {
	if bc == nil || strings.TrimSpace(bc.MaxWallTime) == "" {
		return time.Duration(0), DefaultBudgetMaxWallTime
	}
	d, err := time.ParseDuration(bc.MaxWallTime)
	if err != nil {
		return time.Duration(0), bc.MaxWallTime
	}
	return d, bc.MaxWallTime
}

// EffectiveMaxAttempts возвращает лимит попыток: явный или дефолт.
func (bc *BudgetConfig) EffectiveMaxAttempts() int {
	if bc == nil || bc.MaxAttempts <= 0 {
		return DefaultBudgetMaxAttempts
	}
	return bc.MaxAttempts
}

func (bc *BudgetConfig) Validate() error {
	if bc == nil {
		return nil
	}
	if bc.MaxWallTime != "" {
		d, err := time.ParseDuration(bc.MaxWallTime)
		if err != nil || d <= 0 {
			return fmt.Errorf("budget.max_wall_time %q не парсится (пример: 2h)", bc.MaxWallTime)
		}
	}
	if bc.MaxAttempts < 0 {
		return fmt.Errorf("budget.max_attempts не может быть отрицательным")
	}
	return nil
}

// TreeHashConfig — project-specific настройки tree hashing (OPS-2).
// IgnoreDirs добавляет имена каталогов к каноническому baseline без его
// ослабления. Только простые имена (строгий ValidIgnoreDirName), никаких путей
// или glob'ов.
type TreeHashConfig struct {
	IgnoreDirs []string `yaml:"ignore_dirs,omitempty"`
}

// ValidIgnoreDirName — строгий шаблон имени каталога для tree-hash ignore
// (канонический — в pkg/checks, где живёт tree hashing).
func ValidIgnoreDirName(name string) bool { return checks.ValidIgnoreDirName(name) }

func (tc *TreeHashConfig) Validate() error {
	if tc == nil {
		return nil
	}
	seen := make(map[string]bool, len(tc.IgnoreDirs))
	for _, name := range tc.IgnoreDirs {
		if !checks.ValidIgnoreDirName(name) {
			return fmt.Errorf("tree_hash.ignore_dirs: недопустимое имя каталога %q (допустимо только простое имя: буквы/цифры/-/./_, без '/', не '.'/'..', без ведущего '-')", name)
		}
		if seen[name] {
			return fmt.Errorf("tree_hash.ignore_dirs: дубликат %q", name)
		}
		seen[name] = true
	}
	return nil
}

type ContainmentConfig struct {
	Profile string `yaml:"profile"`
}

var validContainmentProfiles = map[string]bool{
	"trusted-local": true, "strict": true,
}

func (cc *ContainmentConfig) Validate() error {
	if cc == nil {
		return nil
	}
	if cc.Profile == "" {
		cc.Profile = "trusted-local"
	}
	if !validContainmentProfiles[cc.Profile] {
		return fmt.Errorf("containment.profile: неизвестный профиль %q (допустимы: trusted-local, strict)", cc.Profile)
	}
	return nil
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
	// Deferred — см. workflow.ApprovalPolicy.Deferred.
	Deferred bool `yaml:"deferred,omitempty"`
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"schema_version": true, "pipeline": true, "cli": true, "model": true,
		"effort": true, "stage_timeout": true, "workflow": true, "containment": true,
		"tree_hash": true, "budget": true,
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
		TreeHash      *TreeHashConfig `yaml:"tree_hash"`
		Budget        *BudgetConfig   `yaml:"budget"`
	}
	var raw rawConfig
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.SchemaVersion = raw.SchemaVersion
	if c.SchemaVersion == 0 {
		return fmt.Errorf("config: schema_version обязателен (поддерживается только %d)", CurrentSchemaVersion)
	}
	c.CLI = raw.CLI
	c.Model = raw.Model
	c.Effort = raw.Effort
	c.StageTimeout = raw.StageTimeout
	c.Workflow = raw.Workflow
	c.TreeHash = raw.TreeHash
	c.Budget = raw.Budget

	if raw.Pipeline.Kind == 0 {
		return fmt.Errorf("config: pipeline is required")
	}
	if raw.Pipeline.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: pipeline must be a list")
	}
	allowedAgentFields := map[string]bool{
		"name": true, "model": true, "effort": true, "cli": true,
		"timeout": true, "checks": true,
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
		"roles": true, "quorum": true, "actions": true, "deferred": true,
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

// CompiledGraph компилирует schema v4 workflow в immutable runtime contract.
func (c *Config) CompiledGraph() (workflow.Graph, error) {
	graph := workflow.Graph{SchemaVersion: CurrentSchemaVersion}
	for _, stage := range c.PipelineAgents {
		graph.Nodes = append(graph.Nodes, workflow.Node{Name: stage.Name})
	}
	if len(graph.Nodes) == 0 {
		return graph, fmt.Errorf("workflow graph: pipeline пуст")
	}
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
				Roles:    append([]string(nil), edge.Approval.Roles...),
				Quorum:   edge.Approval.Quorum,
				Actions:  copyTargets(edge.Approval.Actions),
				Deferred: edge.Approval.Deferred,
			}
		}
		graph.Edges = append(graph.Edges, compiled)
	}
	if err := graph.Validate(true, true); err != nil {
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
			if cfg.Timeout == "" {
				cfg.Timeout = c.StageTimeout
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
	validate(c.SchemaVersion == CurrentSchemaVersion,
		"schema_version %d не поддерживается (поддерживается только %d; легаси схемы 1–3 удалены)",
		c.SchemaVersion, CurrentSchemaVersion)

	if c.StageTimeout != "" {
		if duration, err := time.ParseDuration(c.StageTimeout); err != nil || duration <= 0 {
			errs = append(errs, fmt.Sprintf("stage_timeout %q не парсится (пример: 30m)", c.StageTimeout))
		}
	}
	validate(isOneOf(c.Effort, "", "low", "medium", "high"),
		"effort %q недопустим (low|medium|high)", c.Effort)
	validate(c.CLI == "" || runtime.AdapterExists(c.CLI),
		"cli %q не поддерживается (неизвестный runtime adapter; доступны: %s)", c.CLI, runtime.AdapterNames())

	seenNames := make(map[string]bool, len(c.PipelineAgents))
	for _, a := range c.PipelineAgents {
		validate(a.Name != "", "имя агента обязательно")
		validate(!seenNames[a.Name], "агент %q повторяется в pipeline", a.Name)
		seenNames[a.Name] = true
		if reg != nil {
			validate(reg.Exists(a.Name), "агент %q не найден в registry", a.Name)
		}
		validate(isOneOf(a.Effort, "", "low", "medium", "high"),
			"%s: effort %q недопустим (low|medium|high)", a.Name, a.Effort)
		validate(a.CLI == "" || runtime.AdapterExists(a.CLI),
			"%s: cli %q не поддерживается (неизвестный runtime adapter)", a.Name, a.CLI)
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
	if c.Containment != nil {
		if err := c.Containment.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.TreeHash != nil {
		if err := c.TreeHash.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Budget != nil {
		if err := c.Budget.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
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
