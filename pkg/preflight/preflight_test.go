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

// TestDeliveryRemoteMissingIsRequiredForDeliveryWorkflow — AUD-09: отсутствие
// remote origin в delivery-workflow классифицируется как required failure
// (delivery_remote). Это единая классификация для CLI, web-контроллера и
// worker — все три ходят в один и тот же pkg/preflight Checker.
func TestDeliveryRemoteMissingIsRequiredForDeliveryWorkflow(t *testing.T) {
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
			}
		}
		return "", os.ErrPermission
	}
	report := checker.Check(context.Background())
	if report.Ready {
		t.Fatal("delivery workflow без origin не должен быть ready")
	}
	for _, check := range report.Checks {
		if check.ID == "delivery_remote" {
			if check.Status != StatusFailed || !check.Required {
				t.Fatalf("delivery_remote должен быть required failed, получено: %s required=%v", check.Status, check.Required)
			}
			if !strings.Contains(check.Message, "origin") {
				t.Fatalf("delivery_remote диагностика должна называть origin: %q", check.Message)
			}
			return
		}
	}
	t.Fatal("классификация missing origin отсутствует (delivery_remote check не найден)")
}

// TestModelDiagnosticNamesSelectedRuntime — AUD-09: диагностика model не
// хардкодит OpenCode: при cli=codex/claude сообщение называет фактический
// рантайм из конфига (или DefaultCLI, если поле пустое).
func TestModelDiagnosticNamesSelectedRuntime(t *testing.T) {
	for _, test := range []struct {
		cli  string
		want string
	}{
		{cli: "codex", want: "codex"},
		{cli: "claude", want: "claude"},
		{cli: "opencode", want: "opencode"},
		{cli: "", want: "opencode"}, // runtime.DefaultCLI
	} {
		checker := testChecker(t, false)
		checker.config.CLI = test.cli
		checker.config.Model = "auto"
		checker.run = func(_ context.Context, name string, args ...string) (string, error) {
			if filepath.Base(name) == "git" {
				return checker.target, nil
			}
			return "version ok", nil
		}
		report := checker.Check(context.Background())
		var message string
		for _, check := range report.Checks {
			if check.ID == "model" {
				message = check.Message
				break
			}
		}
		if !strings.Contains(message, test.want) {
			t.Errorf("cli=%q: модель диагностики должна называть %q, получено %q", test.cli, test.want, message)
		}
		if strings.Contains(message, "OpenCode") && test.want != "opencode" {
			t.Errorf("cli=%q: диагностика не должна хардкодить OpenCode: %q", test.cli, message)
		}
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
