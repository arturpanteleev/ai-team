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
	"github.com/arturpanteleev/ai-team/pkg/runtime"
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

func TestPreflightReportsClaudeAndCodexAuthenticationMethod(t *testing.T) {
	for _, test := range []struct {
		name   string
		cli    string
		method string
		status Status
		setup  func(*testing.T)
	}{
		{
			name:   "claude subscription",
			cli:    "claude",
			method: "способ входа: подписка",
			status: StatusPassed,
			setup: func(t *testing.T) {
				t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-subscription-token")
			},
		},
		{
			name:   "claude API key",
			cli:    "claude",
			method: "способ входа: API-ключ",
			status: StatusPassed,
			setup: func(t *testing.T) {
				t.Setenv("ANTHROPIC_API_KEY", "synthetic-api-key")
				t.Setenv(runtime.HarnessEnvAllowVar, "ANTHROPIC_API_KEY")
			},
		},
		{
			name:   "claude key must be allowed",
			cli:    "claude",
			method: "способ входа: не найден",
			status: StatusWarning,
			setup: func(t *testing.T) {
				t.Setenv("ANTHROPIC_API_KEY", "synthetic-api-key")
			},
		},
		{
			name:   "codex subscription",
			cli:    "codex",
			method: "способ входа: подписка",
			status: StatusPassed,
			setup: func(t *testing.T) {
				home := os.Getenv("HOME")
				path := filepath.Join(home, ".codex")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "codex API key",
			cli:    "codex",
			method: "способ входа: API-ключ",
			status: StatusPassed,
			setup: func(t *testing.T) {
				t.Setenv("OPENAI_API_KEY", "synthetic-api-key")
				t.Setenv(runtime.HarnessEnvAllowVar, "OPENAI_API_KEY")
			},
		},
		{
			name:   "codex no auth",
			cli:    "codex",
			method: "способ входа: не найден",
			status: StatusWarning,
			setup:  func(*testing.T) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("CODEX_API_KEY", "")
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv(runtime.HarnessEnvAllowVar, "")
			t.Setenv(runtime.HarnessEnvAllowLegacyVar, "")
			test.setup(t)

			checker := testChecker(t, false)
			checker.config.CLI = test.cli
			checker.run = func(_ context.Context, name string, args ...string) (string, error) {
				if filepath.Base(name) == "git" {
					if strings.Contains(strings.Join(args, " "), "branch --show-current") {
						return "main", nil
					}
					return checker.target, nil
				}
				return test.cli + " version", nil
			}
			report := checker.Check(context.Background())
			for _, check := range report.Checks {
				if check.ID != "credentials" {
					continue
				}
				if check.Status != test.status || check.Message != test.method {
					t.Fatalf("credentials = %s %q, want %s %q", check.Status, check.Message, test.status, test.method)
				}
				if strings.Contains(check.Message, "synthetic-") {
					t.Fatal("preflight exposed credential contents")
				}
				return
			}
			t.Fatal("credentials check missing from preflight")
		})
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
