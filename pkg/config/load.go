package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Default() *Config {
	return &Config{
		PipelineAgents: []AgentConfig{
			{Name: "analyst"},
			{Name: "architect"},
			{Name: "coder"},
			{Name: "reviewer"},
			{Name: "tester"},
			{Name: "deployer"},
		},
		CLI:   "opencode",
		Model: "auto",
		Effort: "medium",
	}
}
