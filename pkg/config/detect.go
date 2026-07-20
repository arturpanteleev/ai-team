package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

// ApplyDetectedChecks adds explicit, inspectable required checks when a known
// project manifest is present. Unknown stacks remain without guessed commands;
// delivery will then fail closed until checks are configured by the user.
func (c *Config) ApplyDetectedChecks(target string) string {
	var definitions []checks.Definition
	profile := ""
	switch {
	case fileExists(filepath.Join(target, "go.mod")):
		profile = "go"
		definitions = []checks.Definition{
			{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest, Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired, Timeout: "20m"},
			{Name: "go-vet", Class: "lint", Command: []string{"go", "vet", "./..."}, Policy: checks.PolicyRequired, Timeout: "10m"},
		}
	default:
		return ""
	}
	tester := c.findAgent("tester")
	if tester == nil {
		return ""
	}
	tester.Checks = append([]checks.Definition(nil), definitions...)
	return profile
}

func (c *Config) findAgent(name string) *AgentConfig {
	for i := range c.PipelineAgents {
		if c.PipelineAgents[i].Name == name {
			return &c.PipelineAgents[i]
		}
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func npmTestConfigured(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	test := strings.TrimSpace(manifest.Scripts["test"])
	return test != "" && !strings.Contains(test, "no test specified")
}
