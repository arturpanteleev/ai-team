package config

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ErrCodeTrustedConfigOnly is the machine-readable code the web API returns
// when a template submitted through the web (REST, MCP, UI) names a host
// command, a container image or mounts.
const ErrCodeTrustedConfigOnly = "trusted_config_only"

// UntrustedCommandProblems lists what a template submitted through the web
// (validate/publish over REST, MCP or the UI) must not carry, as readable
// problems. The program a repository runs on the host or in a container comes
// only from the repository's own file configuration (.ai-team/config.yaml,
// edited as code), never from a network request:
//
//   - gate references of kind external and docker (external_program,
//     docker_program) are rejected outright: their parameters are the command,
//     the image and the mounts;
//   - executor_adapters, checks and mcp_servers define commands too, and the
//     web template is a whole-document replacement, so a stage may still
//     reference them by name, but each definition must be identical to the
//     same-named definition of the active (trusted) template. A new or changed
//     definition is rejected.
//
// trusted is the active template parsed from the repository, or nil when there
// is none; with nil every command-bearing definition is rejected.
func (c *Config) UntrustedCommandProblems(trusted *Config) []string {
	var problems []string
	registry := gatelib.Default()
	for _, stage := range c.Stages {
		for _, ref := range stage.EffectiveGateGroups().Refs() {
			if isCommandGate(registry, ref) {
				problems = append(problems, fmt.Sprintf("этап %q: гейт %s запускает команду или контейнер", stage.ID, ref.Gate))
			}
		}
	}
	trustedAdapters, trustedChecks, trustedMCP := trustedDefinitions(trusted)
	for _, name := range sortedMapKeys(c.ExecutorAdapters) {
		if !reflect.DeepEqual(c.ExecutorAdapters[name], trustedAdapters[name]) {
			problems = append(problems, fmt.Sprintf("executor_adapters.%s: команда внешнего исполнителя не совпадает с адаптером файлового конфига", name))
		}
	}
	for _, check := range c.Checks {
		if want, ok := trustedChecks[check.Name]; !ok || !reflect.DeepEqual(check, want) {
			problems = append(problems, fmt.Sprintf("checks.%s: команда проверки не совпадает с проверкой файлового конфига", check.Name))
		}
	}
	for _, name := range sortedMapKeys(c.MCPServers) {
		if !reflect.DeepEqual(c.MCPServers[name], trustedMCP[name]) {
			problems = append(problems, fmt.Sprintf("mcp_servers.%s: команда MCP-сервера не совпадает с сервером файлового конфига", name))
		}
	}
	return problems
}

func isCommandGate(registry *gatelib.Registry, ref workflow.GateRef) bool {
	if ref.Gate == gatelib.GateExternalProgram || ref.Gate == gatelib.GateDockerProgram {
		return true
	}
	def, err := registry.Lookup(ref.Gate, ref.Version)
	if err != nil {
		return false
	}
	return def.Kind == workflow.GateKindExternal || def.Kind == workflow.GateKindDocker
}

func trustedDefinitions(trusted *Config) (adapters map[string]any, checkDefs map[string]any, mcp map[string]any) {
	adapters, checkDefs, mcp = map[string]any{}, map[string]any{}, map[string]any{}
	if trusted == nil {
		return
	}
	for name, spec := range trusted.ExecutorAdapters {
		adapters[name] = spec
	}
	for _, check := range trusted.Checks {
		checkDefs[check.Name] = check
	}
	for name, server := range trusted.MCPServers {
		mcp[name] = server
	}
	return
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
