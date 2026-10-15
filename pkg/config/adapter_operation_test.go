package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
)

// S09: an external adapter replaces model execution only; a controller
// operation (delivery, deploy) is rejected when the config is validated.
func TestValidateRejectsAdapterOnControllerOperation(t *testing.T) {
	registry := agent.NewFS(os.DirFS(filepath.Join("..", "..", "agents")))
	for _, name := range []string{"deployer", "cicd-deploy"} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{
				SchemaVersion:  CurrentSchemaVersion,
				Template:       "adapter-operation",
				Title:          "Adapter operation",
				PipelineAgents: []AgentConfig{{Name: name}},
				Stages: []TemplateStage{
					{ID: "ship", Title: "Ship", Function: "developer", Result: "md", Executor: "agent", Agent: name, Adapter: "demo"},
				},
				ExecutorAdapters: map[string]stageexec.AdapterSpec{"demo": {Argv: []string{"./x.sh"}}},
			}
			err := cfg.Validate(registry)
			if err == nil || !strings.Contains(err.Error(), "controller operation") {
				t.Fatalf("adapter on %s must be rejected at validation: %v", name, err)
			}
		})
	}
}
