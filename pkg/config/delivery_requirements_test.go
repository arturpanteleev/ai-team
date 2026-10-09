package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"gopkg.in/yaml.v3"
)

type deliveryValidationRegistry struct {
	agents   map[string]bool
	verdicts map[string]bool
}

func (r deliveryValidationRegistry) Exists(name string) bool {
	return r.agents[name]
}

func (r deliveryValidationRegistry) HasRequiredVerdictContract(name string) (bool, error) {
	return r.verdicts[name], nil
}

func TestAgentConfigRunsOnlyChecksNamedByDeliveryStage(t *testing.T) {
	cfg := &Config{
		Template:       "delivery-checks",
		PipelineAgents: []AgentConfig{{Name: "implementation"}, {Name: "deployer"}},
		Stages: []TemplateStage{
			{ID: "implementation", Agent: "implementation", Delivery: &TemplateDelivery{RequireChecks: []string{"go-test"}}},
			{ID: "deployer", Agent: "deployer"},
		},
		Checks: []checks.Definition{
			{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest, Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired},
			{Name: "optional-lint", Class: "lint", Command: []string{"golangci-lint", "run"}, Policy: checks.PolicyOptional},
		},
	}
	implementation := cfg.AgentConfig("implementation")
	if implementation == nil || len(implementation.Checks) != 1 || implementation.Checks[0].Name != "go-test" {
		t.Fatalf("delivery stage should run exactly its named project check: %+v", implementation)
	}
	if deployer := cfg.AgentConfig("deployer"); deployer == nil || len(deployer.Checks) != 0 {
		t.Fatalf("unrelated delivery agent must not inherit project checks: %+v", deployer)
	}
}

func TestTemplateDeliveryUnmarshalsRequiredVerdictStages(t *testing.T) {
	var stage TemplateStage
	err := yaml.Unmarshal([]byte("id: implementation\ntitle: Implementation\nfunction: developer\nresult: link\nlink_kind: pr\nexecutor: agent\nagent: coder\ndelivery:\n  require_checks: [go-test]\n  require_verdicts: [design_review]\n"), &stage)
	if err != nil {
		t.Fatalf("unmarshal template delivery verdict refs: %v", err)
	}
	if stage.Delivery == nil || len(stage.Delivery.RequireChecks) != 1 || stage.Delivery.RequireChecks[0] != "go-test" ||
		len(stage.Delivery.RequireVerdicts) != 1 || stage.Delivery.RequireVerdicts[0] != "design_review" {
		t.Fatalf("unexpected template delivery fields: %+v", stage.Delivery)
	}
}

func TestTemplateDeliveryVerdictsMustReferenceEarlierStages(t *testing.T) {
	registry := deliveryValidationRegistry{
		agents:   map[string]bool{"implementation": true, "reviewer": true},
		verdicts: map[string]bool{"reviewer": true},
	}
	base := Config{
		SchemaVersion: CurrentSchemaVersion, Template: "delivery-verdicts", Title: "Delivery verdicts",
		Checks: []checks.Definition{{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest, Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired}},
		Stages: []TemplateStage{
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "implementation", Delivery: &TemplateDelivery{RequireChecks: []string{"go-test"}, RequireVerdicts: []string{"review"}}},
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer"},
		},
	}
	if err := base.Validate(registry); err == nil || !strings.Contains(err.Error(), "должен ссылаться на этап до delivery") {
		t.Fatalf("a verdict stage after delivery must fail validation, got %v", err)
	}
	base.Stages[0], base.Stages[1] = base.Stages[1], base.Stages[0]
	if err := base.Validate(registry); err != nil {
		t.Fatalf("an earlier required verdict stage should be allowed: %v", err)
	}
}

func TestTemplateDeliveryChecksMustBeKnownAndRequired(t *testing.T) {
	base := Config{
		SchemaVersion: CurrentSchemaVersion, Template: "delivery-checks", Title: "Delivery checks",
		Stages: []TemplateStage{{
			ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr",
			Executor: "agent", Agent: "implementation", Delivery: &TemplateDelivery{RequireChecks: []string{"go-test"}},
		}},
	}
	registry := deliveryValidationRegistry{agents: map[string]bool{"implementation": true}}
	tests := []struct {
		name        string
		check       checks.Definition
		wantMessage string
	}{
		{
			name:        "unknown check",
			wantMessage: "ссылается на неизвестную проверку \"go-test\"",
		},
		{
			name: "optional check",
			check: checks.Definition{
				Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
				Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyOptional,
			},
			wantMessage: "должна иметь policy required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			if test.check.Name != "" {
				cfg.Checks = []checks.Definition{test.check}
			}
			err := cfg.Validate(registry)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("delivery.require_checks should reject %s, got %v", test.name, err)
			}
		})
	}
}

func TestTemplateDeliveryVerdictRequiresRegistryContract(t *testing.T) {
	cfg := Config{
		SchemaVersion: CurrentSchemaVersion, Template: "delivery-verdicts", Title: "Delivery verdicts",
		Checks: []checks.Definition{{Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest, Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired}},
		Stages: []TemplateStage{
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer"},
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "implementation", Delivery: &TemplateDelivery{RequireChecks: []string{"go-test"}, RequireVerdicts: []string{"review"}}},
		},
	}
	registry := deliveryValidationRegistry{
		agents:   map[string]bool{"implementation": true, "reviewer": true},
		verdicts: map[string]bool{"reviewer": false},
	}
	err := cfg.Validate(registry)
	if err == nil || !strings.Contains(err.Error(), "должен иметь verdict.required contract") {
		t.Fatalf("delivery should reject a required verdict stage without registry contract, got %v", err)
	}
}
