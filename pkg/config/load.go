package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return nil, err
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
	if cfg.SchemaVersion == SchemaVersion2 {
		migrateV2ToV3(&cfg)
	}
	return &cfg, nil
}

func migrateV2ToV3(cfg *Config) {
	for agentIndex := range cfg.PipelineAgents {
		for checkIndex := range cfg.PipelineAgents[agentIndex].Checks {
			check := &cfg.PipelineAgents[agentIndex].Checks[checkIndex]
			if check.Adapter != "" || len(check.Command) < 2 || check.Command[0] != "go" || check.Command[1] != "test" {
				continue
			}
			check.Adapter = checks.AdapterGoTest
			if !hasArgument(check.Command[2:], "-json") {
				check.Command = append(check.Command[:2], append([]string{"-json"}, check.Command[2:]...)...)
			}
			if !hasArgument(check.Command[2:], "-count=1") {
				check.Command = append(check.Command[:2], append([]string{"-count=1"}, check.Command[2:]...)...)
			}
		}
	}
	cfg.SchemaVersion = PreviousSchemaVersion
}

func hasArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

// defaultAgentOverrides carries the per-stage config beyond a bare name for
// the default pipeline. The stage names/order themselves come from
// (*agent.Registry).DefaultPipeline() — the single source of truth openspec's
// verifier-integration spec already names — rather than being duplicated
// here independently.
var defaultAgentOverrides = map[string]AgentConfig{
	"analyst":   {ApprovalRoles: []string{"product_owner"}},
	"architect": {ApprovalRoles: []string{"architect"}},
	"coder":     {MaxRetries: 2, ApprovalRoles: []string{"developer"}},
	"reviewer":  {ApprovalRoles: []string{"reviewer"}},
	"tester":    {ApprovalRoles: []string{"qa"}},
	"verifier":  {ApprovalRoles: []string{"qa"}},
}

func Default() *Config {
	names := (&agent.Registry{}).DefaultPipeline()
	agents := make([]AgentConfig, len(names))
	for i, name := range names {
		cfg := defaultAgentOverrides[name]
		cfg.Name = name
		agents[i] = cfg
	}
	cfg := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: agents,
		CLI:            "opencode",
		Effort:         "medium",
		StageTimeout:   "30m",
	}
	cfg.Workflow = defaultWorkflow(agents)
	for index := range cfg.PipelineAgents {
		cfg.PipelineAgents[index].ApprovalRoles = nil
		cfg.PipelineAgents[index].ApprovalQuorum = ""
		cfg.PipelineAgents[index].MaxRetries = 0
	}
	return cfg
}

func defaultWorkflow(agents []AgentConfig) *WorkflowConfig {
	workflowConfig := &WorkflowConfig{
		Entry:     agents[0].Name,
		MaxVisits: map[string]int{},
	}
	indices := make(map[string]int, len(agents))
	for index, stage := range agents {
		indices[stage.Name] = index
		target := "$complete"
		if index+1 < len(agents) {
			target = agents[index+1].Name
		}
		edge := WorkflowEdgeConfig{From: stage.Name, Outcome: "passed", To: target}
		if target != "$complete" {
			roles := append([]string(nil), stage.ApprovalRoles...)
			if len(roles) == 0 {
				roles = []string{"operator"}
			}
			edge.Approval = &WorkflowApprovalConfig{
				Roles: roles, Quorum: "any",
				Actions: map[string]string{"approve": target, "reject": "$stop"},
			}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, edge)
	}
	for _, source := range []string{"reviewer", "tester", "verifier"} {
		index, exists := indices[source]
		coderIndex, coderExists := indices["coder"]
		if !exists || !coderExists || coderIndex >= index {
			continue
		}
		override := "$complete"
		if index+1 < len(agents) {
			override = agents[index+1].Name
		}
		roles := append([]string(nil), agents[index].ApprovalRoles...)
		if len(roles) == 0 {
			roles = []string{"reviewer"}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, WorkflowEdgeConfig{
			From: source, Outcome: "rejected", To: "coder",
			Approval: &WorkflowApprovalConfig{
				Roles: roles, Quorum: "any",
				Actions: map[string]string{
					"return_to_coder": "coder", "override_approve": override, "reject": "$stop",
				},
			},
		})
	}
	for _, name := range []string{"coder", "reviewer", "tester", "verifier"} {
		if _, exists := indices[name]; exists {
			workflowConfig.MaxVisits[name] = 3
		}
	}
	return workflowConfig
}

// Marshal сериализует конфиг в YAML (используется init-ом: гейты и
// ролевые поля сохраняются, в отличие от ручной сборки строки).
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}
