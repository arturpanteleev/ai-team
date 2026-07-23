package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

// ApplyDetectedChecks adds explicit, inspectable required checks when a known
// project manifest is present. Unknown stacks remain without guessed commands;
// delivery will then fail closed until checks are configured by the user.
//
// Returns the detected profile name ("" if the stack isn't recognized), and
// a non-empty warning if a stack WAS detected but there is no "tester" stage
// to attach the checks to (e.g. a pipeline that renamed or removed it) —
// detection is currently name-based (config.AgentConfig carries no
// mutation/verdict metadata; that only exists on the full agent.Agent
// definition, which init doesn't load a registry for), so this can't be
// resolved silently and must be surfaced to the caller instead.
func (c *Config) ApplyDetectedChecks(target string) (profile string, warning string) {
	var definitions []checks.Definition
	switch {
	case fileExists(filepath.Join(target, "go.mod")):
		profile = "go"
		definitions = []checks.Definition{
			{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest, Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired, Timeout: "20m"},
			{Name: "go-vet", Class: "lint", Command: []string{"go", "vet", "./..."}, Policy: checks.PolicyRequired, Timeout: "10m"},
		}
	default:
		return "", ""
	}
	tester := c.findAgent("tester")
	if tester == nil {
		return profile, fmt.Sprintf(
			"обнаружен %s-профиль, но в pipeline нет стадии \"tester\" — required checks не присвоены. "+
				"Если стадия была переименована, добавьте эти checks в config.yaml вручную; иначе delivery останется запрещён.",
			profile,
		)
	}
	tester.Checks = append([]checks.Definition(nil), definitions...)
	return profile, ""
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
