package artifact

import (
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	p := NewPaths("add-auth", ".ai-team/artifacts")

	if !strings.HasSuffix(p.Task(), "add-auth/task.md") {
		t.Errorf("unexpected task path: %s", p.Task())
	}
	if !strings.HasSuffix(p.ProductSpec(), "add-auth/spec.md") {
		t.Errorf("unexpected product spec path: %s", p.ProductSpec())
	}
	if !strings.HasSuffix(p.TechDesign(), "add-auth/design.md") {
		t.Errorf("unexpected tech design path: %s", p.TechDesign())
	}
	if !strings.HasSuffix(p.Review(), "add-auth/review.md") {
		t.Errorf("unexpected review path: %s", p.Review())
	}
	if !strings.HasSuffix(p.TestReport(), "add-auth/test-report.md") {
		t.Errorf("unexpected test report path: %s", p.TestReport())
	}
	if !strings.HasSuffix(p.Proposal(), "add-auth/proposal.md") {
		t.Errorf("unexpected proposal path: %s", p.Proposal())
	}
}

func TestTask(t *testing.T) {
	task := Task{
		Feature:  "add-auth",
		TaskDesc: "Implement JWT auth",
		ArtifactRoot: ".ai-team/artifacts",
	}
	if task.Feature != "add-auth" {
		t.Errorf("unexpected feature: %s", task.Feature)
	}
}
