package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type AgentConfig struct {
	Name       string `yaml:"name"`
	Model      string `yaml:"model"`
	Effort     string `yaml:"effort"`
	CLI        string `yaml:"cli"`
	Transition string `yaml:"transition"`
	MaxRetries int    `yaml:"max_retries"`
	GateAfter  bool   `yaml:"gate_after"`
	GateBefore bool   `yaml:"gate_before"`
}

type Config struct {
	PipelineAgents []AgentConfig `yaml:"-"`
	CLI            string        `yaml:"cli"`
	Model          string        `yaml:"model"`
	Effort          string        `yaml:"effort"`
	pipelineRaw    interface{}   `yaml:"pipeline"`
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	type rawConfig struct {
		Pipeline     interface{} `yaml:"pipeline"`
		CLI          string      `yaml:"cli"`
		Model        string      `yaml:"model"`
		Effort        string      `yaml:"effort"`
	}
	var raw rawConfig
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.CLI = raw.CLI
	c.Model = raw.Model
	c.Effort = raw.Effort

	if raw.Pipeline == nil {
		return fmt.Errorf("config: pipeline is required")
	}

	switch v := raw.Pipeline.(type) {
	case []interface{}:
		for i, item := range v {
			switch item := item.(type) {
			case string:
				c.PipelineAgents = append(c.PipelineAgents, AgentConfig{Name: item})
			case map[string]interface{}:
				var ac AgentConfig
				node, err := yaml.Marshal(item)
				if err != nil {
					return fmt.Errorf("config: pipeline[%d] marshal: %w", i, err)
				}
				if err := yaml.Unmarshal(node, &ac); err != nil {
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
	default:
		return fmt.Errorf("config: pipeline must be a list")
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
				cfg.Transition = "auto"
			}
			return &cfg
		}
	}
	return nil
}

func (ac *AgentConfig) HasGateAfter() bool {
	return ac.GateAfter
}

func (ac *AgentConfig) HasGateBefore() bool {
	return ac.GateBefore
}
