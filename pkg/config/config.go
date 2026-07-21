package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"gopkg.in/yaml.v3"
)

// Допустимые значения полей (валидируются в Validate).
const (
	CurrentSchemaVersion  = 3
	PreviousSchemaVersion = 2
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
	Checks            []checks.Definition `yaml:"checks,omitempty"`
}

type Config struct {
	SchemaVersion  int           `yaml:"schema_version,omitempty"`
	PipelineAgents []AgentConfig `yaml:"pipeline"`
	CLI            string        `yaml:"cli,omitempty"`
	Model          string        `yaml:"model,omitempty"`
	Effort         string        `yaml:"effort,omitempty"`
	StageTimeout   string        `yaml:"stage_timeout,omitempty"`
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"schema_version": true, "pipeline": true, "cli": true, "model": true,
		"effort": true, "stage_timeout": true,
	}, "config"); err != nil {
		return err
	}
	type rawConfig struct {
		SchemaVersion int       `yaml:"schema_version"`
		Pipeline      yaml.Node `yaml:"pipeline"`
		CLI           string    `yaml:"cli"`
		Model         string    `yaml:"model"`
		Effort        string    `yaml:"effort"`
		StageTimeout  string    `yaml:"stage_timeout"`
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
	validate(c.SchemaVersion == 0 || c.SchemaVersion == LegacySchemaVersion || c.SchemaVersion == PreviousSchemaVersion || c.SchemaVersion == CurrentSchemaVersion,
		"schema_version %d не поддерживается (поддерживаются %d, %d и %d)", c.SchemaVersion, LegacySchemaVersion, PreviousSchemaVersion, CurrentSchemaVersion)

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
		if c.SchemaVersion == CurrentSchemaVersion {
			validate(!a.GateAfter && !a.GateBefore && a.Transition == "",
				"%s: schema_version %d запрещает legacy gate_after/gate_before/transition", a.Name, CurrentSchemaVersion)
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
