package pipeline

import (
	"os"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestMarkQuestionStagesAppliesToPinnedTemplateConfig(t *testing.T) {
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	reg := agent.NewFS(os.DirFS("../../agents"))
	var target string
	for _, stage := range cfg.Stages {
		if stage.Agent == "analyst" {
			target = stage.ID
		}
	}
	if target == "" {
		t.Skip("standard profile has no analyst stage")
	}
	markQuestionStages(cfg, reg)
	if !cfg.QuestionStages[target] {
		t.Fatalf("этап %s с ask_questions агентом должен попасть в QuestionStages: %+v", target, cfg.QuestionStages)
	}
	graph, err := cfg.CompiledGraph()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Edge(target, "blocked"); !ok {
		t.Fatalf("у %s должна быть blocked-петля", target)
	}
}
