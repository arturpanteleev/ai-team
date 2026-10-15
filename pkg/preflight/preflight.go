// Package preflight проверяет готовность локального runtime до создания run.
package preflight

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
)

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
	Readiness string    `json:"readiness"`
	CheckedAt time.Time `json:"checked_at"`
	Checks    []Check   `json:"checks"`
	// Unknown означает, что локальный runtime checker недоступен
	// (например, enqueue-only scheduler mode): готовность воркеров не
	// проверялась и не может считаться true.
	Unknown bool `json:"unknown,omitempty"`
}

// Readiness gives API clients a stable three-state contract. Ready remains
// available for compatibility with older clients.
func ReadinessOf(r Report) string {
	if r.Unknown {
		return "unknown"
	}
	if r.Ready {
		return "ready"
	}
	return "blocked"
}

func (r Report) Error() error {
	if r.Ready {
		return nil
	}
	if r.Unknown {
		return errors.New("preflight неизвестен: локальный runtime checker недоступен")
	}
	var failed []string
	for _, check := range r.Checks {
		if check.Required && check.Status == StatusFailed {
			failed = append(failed, check.ID+": "+check.Message)
		}
	}
	return fmt.Errorf("preflight failed: %s", strings.Join(failed, "; "))
}

// CommandRunner исполняет внешнюю команду и возвращает её объединённый вывод.
// Экспортирован, чтобы тесты вне пакета проверяли классификацию исходов, не
// порождая настоящих процессов: иначе результат зависит от загрузки машины, а
// не от проверяемой логики.
type CommandRunner func(context.Context, string, ...string) (string, error)

// LookPath резолвит имя команды в путь.
type LookPath func(string) (string, error)

type Checker struct {
	config   *config.Config
	registry *agent.Registry
	target   string
	run      CommandRunner
	lookPath LookPath
	timeout  time.Duration
	// controllerDelivery: delivery (push, gh) runs in the controller, not in
	// this process, so this checker must not require gh/origin here.
	controllerDelivery bool
}

// Option настраивает Checker. Опции применяются после значений из конфига,
// поэтому явно заданный timeout в тесте перекрывает конфигурационный.
type Option func(*Checker)

// WithCommandRunner подменяет исполнение внешних команд.
func WithCommandRunner(runner CommandRunner) Option {
	return func(c *Checker) {
		if runner != nil {
			c.run = runner
		}
	}
}

// WithLookPath подменяет резолвинг команд в PATH.
func WithLookPath(lookPath LookPath) Option {
	return func(c *Checker) {
		if lookPath != nil {
			c.lookPath = lookPath
		}
	}
}

// WithTimeout задаёт бюджет одной внешней команды в обход конфигурации.
// Неположительное значение игнорируется: бюджет не может быть отключён.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Checker) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithControllerDelivery marks a checker running inside a worker whose
// delivery is performed by the controller through the controller API. Such a
// worker has no GitHub credentials or network, so gh/origin are not checked
// here; the controller's own preflight keeps requiring them.
func WithControllerDelivery() Option {
	return func(c *Checker) { c.controllerDelivery = true }
}

func New(cfg *config.Config, registry *agent.Registry, target string, options ...Option) *Checker {
	checker := &Checker{
		config: cfg, registry: registry, target: target,
		run: runCommand, lookPath: exec.LookPath,
		timeout: cfg.EffectivePreflightTimeout(),
	}
	for _, option := range options {
		option(checker)
	}
	return checker
}

func (c *Checker) Check(ctx context.Context) Report {
	report := Report{Ready: true, CheckedAt: time.Now().UTC()}
	add := func(check Check) {
		report.Checks = append(report.Checks, check)
		if check.Required && check.Status == StatusFailed {
			report.Ready = false
		}
	}

	adapters := c.usedAdapters()
	if c.needsAgentCLI() {
		cli := strings.TrimSpace(c.config.CLI)
		if cli == "" {
			cli = runtime.DefaultCLI
		}
		if !runtime.AdapterExists(filepath.Base(cli)) {
			add(Check{ID: "cli", Status: StatusFailed, Required: true, Message: "configured CLI не поддерживается (нет runtime adapter: " + filepath.Base(cli) + ")"})
		} else if resolved, err := c.lookPath(cli); err != nil {
			add(Check{ID: "cli", Status: StatusFailed, Required: true, Message: "команда " + filepath.Base(cli) + " не найдена в PATH"})
		} else if version, err := c.command(ctx, resolved, "--version"); err != nil {
			add(Check{ID: "cli", Status: StatusFailed, Required: true, Message: "не удалось получить версию: " + safeError(err)})
		} else {
			add(Check{ID: "cli", Status: StatusPassed, Required: true, Message: firstLine(version)})
		}

		model := strings.TrimSpace(c.config.Model)
		if model == "" || model == "auto" {
			// Диагностика ссылается на фактически выбранный CLI-рантайм из
			// конфига (AUD-09): не хардкодим "OpenCode", если cli=codex/claude.
			add(Check{ID: "model", Status: StatusWarning, Message: "model/provider выбирает " + filepath.Base(cli)})
		} else {
			add(Check{ID: "model", Status: StatusPassed, Message: model})
		}

		if cliName := filepath.Base(cli); cliName == "claude" || cliName == "codex" {
			method := runtime.DetectAuthentication(cliName)
			// Без способа входа рантайм падает на старте (изолированный
			// CLAUDE_CONFIG_DIR не видит обычный логин), поэтому прогон не стартует.
			message := "способ входа: не найден"
			status := StatusFailed
			switch method {
			case runtime.AuthenticationAPIKey:
				message, status = "способ входа: API-ключ", StatusPassed
			case runtime.AuthenticationSubscription:
				message, status = "способ входа: подписка", StatusPassed
			}
			add(Check{ID: "credentials", Status: status, Required: true, Message: message})
		} else {
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
		}
	} else {
		add(Check{ID: "cli", Status: StatusPassed, Message: "все agent-этапы исполняют внешние адаптеры; CLI агента не требуется"})
	}
	for _, name := range adapters {
		add(c.adapterCheck(name))
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

	if c.controllerDelivery && c.hasDelivery() {
		add(Check{ID: "delivery", Status: StatusPassed, Message: "delivery выполняет контроллер; gh и origin проверяются на его стороне"})
	} else if c.hasDelivery() {
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
	report.Readiness = ReadinessOf(report)
	return report
}

func (c *Checker) command(parent context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
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

// needsAgentCLI reports whether any stage still runs through the model CLI.
// Only a template whose every non-controller agent stage names an adapter can
// skip the CLI and its credentials; a mixed or legacy config keeps them required.
func (c *Checker) needsAgentCLI() bool {
	if len(c.config.Stages) == 0 {
		return true
	}
	adapterStages := 0
	for _, stage := range c.config.Stages {
		if stage.Executor != "agent" || stage.Agent == "" {
			continue
		}
		if stage.Adapter != "" {
			adapterStages++
			continue
		}
		if definition, err := c.registry.Load(stage.Agent); err == nil && definition.ControllerOperation() {
			continue
		}
		return true
	}
	return adapterStages == 0
}

// usedAdapters lists the adapter names referenced by stages, sorted.
func (c *Checker) usedAdapters() []string {
	seen := map[string]bool{}
	var names []string
	for _, stage := range c.config.Stages {
		if stage.Adapter != "" && !seen[stage.Adapter] {
			seen[stage.Adapter] = true
			names = append(names, stage.Adapter)
		}
	}
	sort.Strings(names)
	return names
}

// adapterCheck verifies that the adapter program exists and is executable.
// A relative path with a separator resolves from the target repository and
// must stay inside it; a bare name resolves through PATH.
func (c *Checker) adapterCheck(name string) Check {
	id := "adapter:" + name
	fail := func(message string) Check {
		return Check{ID: id, Status: StatusFailed, Required: true, Message: message}
	}
	spec, ok := c.config.ExecutorAdapters[name]
	if !ok || len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return fail("адаптер " + name + " не объявлен в executor_adapters")
	}
	if spec.Docker != nil {
		return c.dockerAdapterCheck(id, spec)
	}
	program := spec.Argv[0]
	path := program
	switch {
	case filepath.IsAbs(program):
	case strings.ContainsRune(program, '/') || strings.ContainsRune(program, filepath.Separator):
		root, err := filepath.EvalSymlinks(c.target)
		if err != nil {
			return fail("каталог проекта недоступен: " + safeError(err))
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(root, program))
		if err != nil {
			return fail("программа адаптера " + program + " не найдена")
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fail("программа адаптера " + program + " выходит за пределы репозитория")
		}
		path = resolved
	default:
		resolved, err := c.lookPath(program)
		if err != nil {
			return fail("команда адаптера " + program + " не найдена в PATH")
		}
		return Check{ID: id, Status: StatusPassed, Required: true, Message: resolved}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return fail("программа адаптера " + program + " не найдена")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fail("программа адаптера " + program + " не исполняема")
	}
	return Check{ID: id, Status: StatusPassed, Required: true, Message: program}
}

// dockerAdapterCheck verifies a container executor: argv is a path inside the
// image, so only the docker CLI and the repository paths mounted into the
// container can be checked before a run.
func (c *Checker) dockerAdapterCheck(id string, spec stageexec.AdapterSpec) Check {
	fail := func(message string) Check {
		return Check{ID: id, Status: StatusFailed, Required: true, Message: message}
	}
	docker, err := c.lookPath("docker")
	if err != nil {
		return fail("docker CLI не найден в PATH (нужен контейнерному исполнителю)")
	}
	root, err := filepath.EvalSymlinks(c.target)
	if err != nil {
		return fail("каталог проекта недоступен: " + safeError(err))
	}
	for _, mount := range spec.Docker.Mounts {
		source, _, _ := strings.Cut(mount, ":")
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(source))); err != nil {
			return fail("путь " + source + " для монтирования в контейнер не найден в репозитории")
		}
	}
	return Check{ID: id, Status: StatusPassed, Required: true, Message: "docker: " + docker + ", образ " + spec.Docker.Image}
}

// hasDelivery reports whether the workflow publishes through the controller
// delivery path: either a delivery-kind agent or a template stage with a
// delivery block (for example an agent implementation stage whose controller
// creates the PR before review). Both push to origin and call gh.
func (c *Checker) hasDelivery() bool {
	for _, stage := range c.config.Stages {
		if stage.Delivery != nil {
			return true
		}
	}
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
	for _, raw := range []string{
		os.Getenv(runtime.HarnessEnvAllowVar),
		os.Getenv(runtime.HarnessEnvAllowLegacyVar),
	} {
		for _, entry := range strings.Split(raw, ",") {
			name := strings.TrimSpace(entry)
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
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
