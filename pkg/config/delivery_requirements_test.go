package config

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"gopkg.in/yaml.v3"
)

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
	base := Config{
		SchemaVersion: CurrentSchemaVersion, Template: "delivery-verdicts", Title: "Delivery verdicts",
		Stages: []TemplateStage{
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "agent", Agent: "implementation", Delivery: &TemplateDelivery{RequireChecks: []string{"go-test"}, RequireVerdicts: []string{"review"}}},
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer"},
		},
	}
	if err := base.validateTemplate(nil); err == nil || !strings.Contains(err.Error(), "должен ссылаться на этап до delivery") {
		t.Fatalf("a verdict stage after delivery must fail validation, got %v", err)
	}
	base.Stages[0], base.Stages[1] = base.Stages[1], base.Stages[0]
	if err := base.validateTemplate(nil); err != nil {
		t.Fatalf("an earlier required verdict stage should be allowed: %v", err)
	}
}
