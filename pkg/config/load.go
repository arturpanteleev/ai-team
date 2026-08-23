package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/agent"
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
	return &cfg, nil
}

// defaultStageRoles — human-роли рёбер default workflow. Источник имён/порядка
// стадий — (*agent.Registry).DefaultPipeline(); роли нужны только для
// генерации approval policies, в AgentConfig не пишутся.
var defaultStageRoles = map[string][]string{
	"analyst":   {"product_owner"},
	"architect": {"architect"},
	"coder":     {"developer"},
	"reviewer":  {"reviewer"},
	"tester":    {"qa"},
	"verifier":  {"qa"},
}

// defaultLoopbackSources — стадии, чей негативный вердикт возвращается к coder.
var defaultLoopbackSources = []string{"reviewer", "tester", "verifier"}

func Default() *Config {
	names := (&agent.Registry{}).DefaultPipeline()
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
	cfg.Workflow = defaultWorkflow(names)
	return cfg
}

func defaultWorkflow(names []string) *WorkflowConfig {
	workflowConfig := &WorkflowConfig{
		Entry:     names[0],
		MaxVisits: map[string]int{},
	}
	indices := make(map[string]int, len(names))
	for index, name := range names {
		indices[name] = index
		target := "$complete"
		if index+1 < len(names) {
			target = names[index+1]
		}
		edge := WorkflowEdgeConfig{From: name, Outcome: "passed", To: target}
		if target != "$complete" {
			roles := append([]string(nil), defaultStageRoles[name]...)
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
	coderIndex, coderExists := indices["coder"]
	for _, source := range defaultLoopbackSources {
		index, exists := indices[source]
		if !coderExists || !exists || index <= coderIndex {
			continue
		}
		override := "$complete"
		if index+1 < len(names) {
			override = names[index+1]
		}
		roles := append([]string(nil), defaultStageRoles[source]...)
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
