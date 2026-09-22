// Тесты в отдельном пакете: они пользуются только экспортированным API и тем
// самым проверяют ровно то, ради чего точки внедрения были экспортированы —
// возможность проверить классификацию исходов извне пакета, не порождая
// настоящих процессов. Пока подмена была неэкспортируемой, такой тест был
// вынужден класть shell-скрипт в PATH и зависел от загрузки машины, а не от
// проверяемой логики (PDD-01).
package preflight_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
)

func registry(t *testing.T) *agent.Registry {
	t.Helper()
	return agent.NewFS(fstest.MapFS{
		"probe/def.yaml": &fstest.MapFile{Data: []byte("name: probe\nruntime: agentcli\nmutation: none\n")},
	})
}

func checkByID(t *testing.T, report preflight.Report, id string) preflight.Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("проверка %q отсутствует в отчёте", id)
	return preflight.Check{}
}

// Истёкший бюджет по-прежнему обязан давать failed required check: контракт
// «Version command зависла» из opencode-integration не ослаблен, изменилась
// только величина бюджета и способ её задать.
func TestVersionProbeTimeoutStaysBlocking(t *testing.T) {
	cfg := &config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: "probe"}}}
	checker := preflight.New(cfg, registry(t), t.TempDir(),
		preflight.WithLookPath(func(name string) (string, error) { return "/usr/bin/" + name, nil }),
		preflight.WithCommandRunner(func(ctx context.Context, name string, args ...string) (string, error) {
			if len(args) == 1 && args[0] == "--version" {
				return "", context.DeadlineExceeded
			}
			return "", errors.New("не относится к делу")
		}),
	)

	cli := checkByID(t, checker.Check(context.Background()), "cli")
	if cli.Status != preflight.StatusFailed || !cli.Required {
		t.Fatalf("истёкший бюджет должен давать failed required check, получено: %+v", cli)
	}
	if cli.Message != "не удалось получить версию: timeout" {
		t.Fatalf("сообщение должно называть причиной таймаут, получено: %q", cli.Message)
	}
}

// Бюджет берётся из конфигурации: без этого единственный способ повлиять на
// него — пересборка бинарника.
func TestBudgetComesFromConfig(t *testing.T) {
	cfg := &config.Config{
		CLI: "opencode", PreflightTimeout: "250ms",
		PipelineAgents: []config.AgentConfig{{Name: "probe"}},
	}
	var observed time.Duration
	checker := preflight.New(cfg, registry(t), t.TempDir(),
		preflight.WithLookPath(func(name string) (string, error) { return "/usr/bin/" + name, nil }),
		preflight.WithCommandRunner(func(ctx context.Context, name string, args ...string) (string, error) {
			if deadline, ok := ctx.Deadline(); ok && observed == 0 {
				observed = time.Until(deadline)
			}
			return "1.0.0", nil
		}),
	)
	checker.Check(context.Background())

	if observed <= 0 || observed > 250*time.Millisecond {
		t.Fatalf("бюджет команды должен следовать preflight_timeout=250ms, получено: %v", observed)
	}
}

// Дефолт применяется, когда поле не задано, и он заведомо больше прежней
// пятисекундной константы: холодный старт рантайма по умолчанию занимает
// секунды, и тесный бюджет делал работоспособный рантайм неотличимым от
// отсутствующего.
func TestDefaultBudgetExceedsColdStart(t *testing.T) {
	if config.DefaultPreflightTimeout <= 5*time.Second {
		t.Fatalf("дефолтный бюджет %v не даёт запаса к холодному старту рантайма", config.DefaultPreflightTimeout)
	}
	cfg := &config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: "probe"}}}
	if effective := cfg.EffectivePreflightTimeout(); effective != config.DefaultPreflightTimeout {
		t.Fatalf("пустое поле должно давать дефолт, получено: %v", effective)
	}
	cfg.PreflightTimeout = "не длительность"
	if effective := cfg.EffectivePreflightTimeout(); effective != config.DefaultPreflightTimeout {
		t.Fatalf("непарсящееся значение должно давать дефолт, а не нулевой бюджет, получено: %v", effective)
	}
}

// Бюджет нельзя отключить: неположительная опция игнорируется, иначе внешняя
// команда могла бы висеть бесконечно и preflight перестал бы быть гарантией.
func TestBudgetCannotBeDisabled(t *testing.T) {
	cfg := &config.Config{CLI: "opencode", PipelineAgents: []config.AgentConfig{{Name: "probe"}}}
	var hadDeadline bool
	checker := preflight.New(cfg, registry(t), t.TempDir(),
		preflight.WithTimeout(0),
		preflight.WithLookPath(func(name string) (string, error) { return "/usr/bin/" + name, nil }),
		preflight.WithCommandRunner(func(ctx context.Context, name string, args ...string) (string, error) {
			_, hadDeadline = ctx.Deadline()
			return "1.0.0", nil
		}),
	)
	checker.Check(context.Background())
	if !hadDeadline {
		t.Fatal("нулевая опция не должна снимать deadline с внешней команды")
	}
}
