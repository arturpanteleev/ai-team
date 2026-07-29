// Package preflight проверяет готовность локального runtime до создания run.
package preflight

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

const commandTimeout = 5 * time.Second
const maxCommandOutput = 4096

type Status string

const (
	StatusPassed  Status = "passed"
	StatusWarning Status = "warning"
	StatusFailed  Status = "failed"
)

type Check struct {
	ID       string `json:"id"`
	Status   Status `json:"status"`
	Required bool   `json:"required"`
	Message  string `json:"message"`
}

type Report struct {
	Ready     bool      `json:"ready"`
	CheckedAt time.Time `json:"checked_at"`
	Checks    []Check   `json:"checks"`
}

func (r Report) Error() error {
	if r.Ready {
		return nil
	}
	var failed []string
	for _, check := range r.Checks {
		if check.Required && check.Status == StatusFailed {
			failed = append(failed, check.ID+": "+check.Message)
		}
	}
	return fmt.Errorf("preflight failed: %s", strings.Join(failed, "; "))
}

type commandRunner func(context.Context, string, ...string) (string, error)

type Checker struct {
	config   *config.Config
	registry *agent.Registry
	target   string
	run      commandRunner
	lookPath func(string) (string, error)
}

func New(cfg *config.Config, registry *agent.Registry, target string) *Checker {
	return &Checker{config: cfg, registry: registry, target: target, run: runCommand, lookPath: exec.LookPath}
}

func (c *Checker) Check(ctx context.Context) Report {
	report := Report{Ready: true, CheckedAt: time.Now().UTC()}
	add := func(check Check) {
		report.Checks = append(report.Checks, check)
		if check.Required && check.Status == StatusFailed {
			report.Ready = false
		}
	}

	cli := strings.TrimSpace(c.config.CLI)
	if cli == "" {
		cli = "opencode"
	}
	if filepath.Base(cli) != "opencode" {
		add(Check{ID: "opencode", Status: StatusFailed, Required: true, Message: "configured CLI не поддерживается"})
	} else if resolved, err := c.lookPath(cli); err != nil {
		add(Check{ID: "opencode", Status: StatusFailed, Required: true, Message: "команда opencode не найдена в PATH"})
	} else if version, err := c.command(ctx, resolved, "--version"); err != nil {
		add(Check{ID: "opencode", Status: StatusFailed, Required: true, Message: "не удалось получить версию: " + safeError(err)})
	} else {
		add(Check{ID: "opencode", Status: StatusPassed, Required: true, Message: firstLine(version)})
	}

	model := strings.TrimSpace(c.config.Model)
	if model == "" || model == "auto" {
		add(Check{ID: "model", Status: StatusWarning, Message: "model/provider выбирает OpenCode"})
	} else {
		add(Check{ID: "model", Status: StatusPassed, Message: model})
	}

	allowed := allowedCredentialNames()
	if len(allowed) == 0 {
		add(Check{ID: "credentials", Status: StatusWarning, Message: "явные credential environment variables не разрешены"})
	} else {
		var present, missing []string
		for _, name := range allowed {
			if _, ok := os.LookupEnv(name); ok {
				present = append(present, name)
			} else {
				missing = append(missing, name)
			}
		}
		message := "заданы: " + strings.Join(present, ", ")
		status := StatusPassed
		if len(missing) > 0 {
			status = StatusWarning
			message += "; отсутствуют: " + strings.Join(missing, ", ")
		}
		add(Check{ID: "credentials", Status: status, Message: strings.TrimSpace(message)})
	}

	if output, err := c.git(ctx, "rev-parse", "--show-toplevel"); err != nil {
		add(Check{ID: "git_repository", Status: StatusFailed, Required: true, Message: "target не является доступным Git repository"})
	} else {
		add(Check{ID: "git_repository", Status: StatusPassed, Required: true, Message: filepath.Clean(firstLine(output))})
	}
	if output, err := c.git(ctx, "branch", "--show-current"); err != nil || firstLine(output) == "" {
		add(Check{ID: "git_branch", Status: StatusFailed, Required: true, Message: "не удалось определить текущую branch"})
	} else {
		add(Check{ID: "git_branch", Status: StatusPassed, Required: true, Message: firstLine(output)})
	}

	if c.hasDelivery() {
		if output, err := c.git(ctx, "remote", "get-url", "origin"); err != nil || firstLine(output) == "" {
			add(Check{ID: "delivery_remote", Status: StatusFailed, Required: true, Message: "remote origin не настроен"})
		} else {
			add(Check{ID: "delivery_remote", Status: StatusPassed, Required: true, Message: firstLine(output)})
		}
		if resolved, err := c.lookPath("gh"); err != nil {
			add(Check{ID: "github_auth", Status: StatusFailed, Required: true, Message: "команда gh не найдена в PATH"})
		} else if _, err := c.command(ctx, resolved, "auth", "status"); err != nil {
			add(Check{ID: "github_auth", Status: StatusFailed, Required: true, Message: "gh authentication недоступна: " + safeError(err)})
		} else {
			add(Check{ID: "github_auth", Status: StatusPassed, Required: true, Message: "gh authentication доступна"})
		}
	} else {
		add(Check{ID: "delivery", Status: StatusPassed, Message: "delivery stage отсутствует"})
	}
	return report
}

func (c *Checker) command(parent context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	return c.run(ctx, name, args...)
}

func (c *Checker) git(ctx context.Context, args ...string) (string, error) {
	resolved, err := c.lookPath("git")
	if err != nil {
		return "", err
	}
	args = append([]string{"-C", c.target}, args...)
	return c.command(ctx, resolved, args...)
}

func (c *Checker) hasDelivery() bool {
	for _, configured := range c.config.PipelineAgents {
		definition, err := c.registry.Load(configured.Name)
		if err == nil && definition.Kind == "delivery" {
			return true
		}
	}
	return false
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		return output.String(), ctx.Err()
	}
	return output.String(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(value []byte) (int, error) {
	accepted := value
	if remaining := maxCommandOutput - b.Len(); len(accepted) > remaining {
		if remaining < 0 {
			remaining = 0
		}
		accepted = accepted[:remaining]
	}
	_, _ = b.Buffer.Write(accepted)
	return len(value), nil
}

func allowedCredentialNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, raw := range strings.Split(os.Getenv("AI_TEAM_OPENCODE_ENV_ALLOW"), ",") {
		name := strings.TrimSpace(raw)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	if value == "" {
		return "версия не указана"
	}
	return value
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	if err == context.DeadlineExceeded {
		return "timeout"
	}
	return "команда завершилась с ошибкой"
}
