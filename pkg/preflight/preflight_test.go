package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestCredentialReportDoesNotExposeValue(t *testing.T) {
	t.Setenv("AI_TEAM_OPENCODE_ENV_ALLOW", "SECRET_TOKEN,MISSING_TOKEN")
	t.Setenv("SECRET_TOKEN", "do-not-leak")
	checker := testChecker(t, false)
	report := checker.Check(context.Background())
	encoded := ""
	for _, check := range report.Checks {
		encoded += check.Message
	}
	if strings.Contains(encoded, "do-not-leak") {
		t.Fatal("preflight раскрыл credential value")
	}
	if !strings.Contains(encoded, "SECRET_TOKEN") || !strings.Contains(encoded, "MISSING_TOKEN") {
		t.Fatalf("credential names отсутствуют: %s", encoded)
	}
}

func TestDeliveryRequiresGitHubAuthentication(t *testing.T) {
	checker := testChecker(t, true)
	checker.run = func(_ context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "opencode" {
			return "opencode 1.2.3", nil
		}
		if filepath.Base(name) == "git" {
			switch strings.Join(args, " ") {
			case "-C " + checker.target + " rev-parse --show-toplevel":
				return checker.target, nil
			case "-C " + checker.target + " branch --show-current":
				return "main", nil
			case "-C " + checker.target + " remote get-url origin":
				return "git@example.test:repo.git", nil
			}
		}
		return "", os.ErrPermission
	}
	report := checker.Check(context.Background())
	if report.Ready {
		t.Fatal("delivery без gh auth не должен быть ready")
	}
}

func testChecker(t *testing.T, delivery bool) *Checker {
	t.Helper()
	target := t.TempDir()
	git(t, target, "init")
	git(t, target, "config", "user.email", "test@example.com")
	git(t, target, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, target, "add", "README.md")
	git(t, target, "commit", "-m", "initial")

	kind := ""
	if delivery {
		kind = "kind: delivery\nmutation: external\nruntime: delivery\ninputs:\n  review: review.md\noutputs:\n  plan: plan.json\npreconditions:\n  review:\n    required: true\n    marker: Verdict\n    values: [APPROVED]\n"
	}
	registry := agent.NewFS(fstest.MapFS{
		"worker/def.yaml": &fstest.MapFile{Data: []byte("name: worker\nruntime: agentcli\nmutation: none\n")},
		"ship/def.yaml":   &fstest.MapFile{Data: []byte("name: ship\n" + kind)},
	})
	name := "worker"
	if delivery {
		name = "ship"
	}
	checker := New(&config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: name}}}, registry, target)
	checker.lookPath = func(name string) (string, error) { return name, nil }
	checker.run = func(_ context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "opencode" {
			return "opencode 1.2.3", nil
		}
		command := exec.Command(name, args...)
		command.Dir = target
		output, err := command.CombinedOutput()
		return string(output), err
	}
	return checker
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
