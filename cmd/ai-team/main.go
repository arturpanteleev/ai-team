package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	agentdata "github.com/arturpanteleev/ai-team"
	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/artifactstore"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
	"github.com/arturpanteleev/ai-team/pkg/eval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/export"
	"github.com/arturpanteleev/ai-team/pkg/gate"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/scheduler"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/web"
	webstore "github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/worker"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var version = "dev"

// Exit-коды run (см. спеку cli-interface).
const (
	exitOK          = 0
	exitFailed      = 1
	exitBlocked     = 2
	exitUserStopped = 3
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	applyOutputMode()

	switch os.Args[1] {
	case "init":
		cmdInit()
	case "run":
		cmdRun()
	case "decision":
		cmdDecision()
	case "auth-token":
		cmdAuthToken()
	case "worker":
		cmdWorker()
	case "scheduler-worker":
		cmdSchedulerWorker()
	case "list":
		cmdList()
	case "usage":
		cmdUsage()
	case "export":
		cmdExport()
	case "verify":
		cmdVerify()
	case "deliver":
		cmdDeliver()
	case "gate":
		cmdGate()
	case "eval":
		cmdEval()
	case "web":
		cmdWeb()
	case "gc":
		cmdGC()
	case "version":
		fmt.Println(version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`ai-team — AI-команда для spec-driven разработки

Использование:
  ai-team init [--target <path>] [--write-gitignore]
                                     Инициализировать .ai-team/ в проекте
  ai-team run                      Запустить пайплайн агентов
  ai-team decision                 Записать решение человека по pending approval
  ai-team auth-token               Выпустить короткоживущий cloud access token
  ai-team worker                   Выполнить один disposable worker job из stdin
  ai-team scheduler-worker         Claim и выполнить job из persistent queue
  ai-team list                     Список доступных агентов
  ai-team usage <run_id>           Usage-сводка завершённого run (этапы, попытки, время)
  ai-team export <run_id>          Собрать проверенный portable bundle терминального run
                                   (whitelisted typed records и digests; публикует
                                   verified-запись state/exports)
  ai-team verify [--target <dir>] <run_id>
                                   Проверить evidence терминального run (anchor, chain, попытки, attestation)
  ai-team verify <bundle-dir>      Проверить portable bundle самодостаточно (без repo и .ai-team)
  ai-team deliver               Повторить отложенную (deferred) доставку завершённого run:
                                   --run <run_id> --target <.ai-team root> [--feature <name>]
  ai-team gate                     Deterministic diff-policy gate для trusted local
                                   base/candidate (typed checks + attestation bundle)
  ai-team eval                     Оценить качество артефакта или агента
  ai-team web                      Запустить web-дашборд
  ai-team gc                       Уборка растущих артефактов .ai-team
  ai-team version                  Версия
  ai-team help                     Эта справка

Флаги usage:
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)

Глобальные флаги вывода (OPS-6):
  --json                    Стабильные machine-readable JSON records на stdout
  --quiet, -q               Подавить второстепенный человеческий вывод

Флаги export:
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)
  --out <path>              Каталог bundle (по умолчанию .ai-team/exports/<run_id>.bundle)
  --sign-key <path>         ed25519 private key (PEM PKCS8/raw) для DSSE-подписи bundle (P1-5)

Флаги gc:
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)
  --older-than <duration>   Возраст terminal-ранов для уборки state (по умолчанию 720h)
  --keep-last <n>           Защитить n самых свежих terminal-ранов (по умолчанию 20)
  --dry-run                 Только показать план: что удалится и сколько байт
  --prune-runs              Разрешить удаление immutable run evidence (.ai-team/runs),
                            но только для run с verified-записью state/exports (V0-4 guard;
                            пока нет экспорта — флаг безопасно не удаляет evidence)

Флаги gate:
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)
  --base <ref>              Базовый ref, только trusted local (по умолчанию HEAD)
  --candidate <ref|WORKTREE> Кандидат: локальный ref или WORKTREE (по умолчанию)
  --config <path>           Gate config (по умолчанию gate.yaml в target, затем
                            .ai-team/gate.yaml; иначе — дефолты: test_modify required)
  --out <path>              Каталог attestation bundle (по умолчанию
                            <target>/.ai-team/gates/<ts> или gate-out/<ts>)
  --sign-key <path>         ed25519 private key (PEM PKCS8/raw) для DSSE-подписи bundle (P1-5)

Флаги verify:
  --verify-key <path>       ed25519 public key (PEM/raw) — требовать валидную
                            DSSE-подпись bundle (fail-closed, P1-5)

Exit-коды gate: 0 — PASS, 1 — FAIL (diff-policy/required checks), 2 — BLOCKED
                (конфиг, отсутствующий/нелокальный ref, untrusted — запрещён до P1-4)

Флаги run:
  --feature <name>          Имя фичи (буквы, цифры, "-", "_", ".")
  --task <description>      Описание задачи
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)
	  --resume <run_id>         Продолжить non-terminal run с той же identity
	  --approve-gates           Явно подтвердить обычные gate-точки в non-interactive режиме
	                            (в default-профилях forward-гейты отложены — одно consolidated
	                            delivery-решение на весь run, APF-1)
	  --approve-plan <sha256>    Разрешить только показанный canonical delivery plan
	                            (ratify-ит и все отложенные гейты run'а)

Exit-коды run: 0 — успех, 1 — ошибка или негативный вердикт,
               2 — BLOCKED (нужно вмешательство), 3 — остановлен пользователем

Флаги eval:
  --agent <name>            Имя агента
  --artifact <path>         Путь к артефакту для оценки (без запуска пайплайна)
  --feature <name>          Запустить одного агента и оценить его артефакты
  --task <description>      Описание задачи для запуска
  --target <path>           Путь к проекту (по умолчанию текущая директория)
	  --samples <1-20>          Число независимых LLM-оценок (advisory)
	  --json-out <path>         Путь JSON evidence (по умолчанию .ai-team/evals/...)

Флаги web:
	  --target <path>           Путь к целевому проекту (по умолчанию текущий)
	  --port <port>             Порт (по умолчанию 8080)
	  --host <host>             Адрес bind (по умолчанию 127.0.0.1)
  --db <path>               Путь к SQLite (по умолчанию .ai-team/web.db)
  --dist <path>             Каталог собранного frontend (по умолчанию web/dist)
  --artifacts <path>        Корень артефактов (по умолчанию .ai-team/artifacts)
  --auth-secret-env <name>  Env с HMAC secret; если задан, включает cloud auth
  --worker-command <path>   Запускать pipeline в отдельном worker process
  --scheduler-db <path>     Enqueue run в persistent scheduler вместо inline execution

Флаги auth-token:
  --actor <id>              Immutable actor identity
  --roles <csv>             product_owner, architect, developer, reviewer, qa,
                            release_manager
  --ttl <duration>          Срок token, максимум 24h (по умолчанию 1h)
  --secret-env <name>       Env с HMAC secret (по умолчанию AI_TEAM_AUTH_SECRET)

Флаги scheduler-worker:
  --scheduler-db <path>     Persistent queue SQLite
  --artifact-store <path>   Persistent SHA-256 CAS root
  --worker-command <path>   Executable ai-team worker
  --worker-id <id>          Уникальный lease owner
  --once                    Выполнить не более одного claim`)
}

func cmdAuthToken() {
	flags := flag.NewFlagSet("auth-token", flag.ExitOnError)
	actorID := flags.String("actor", "", "Immutable actor identity")
	roleValues := flags.String("roles", "", "Список ролей через запятую")
	ttl := flags.Duration("ttl", time.Hour, "Срок token")
	secretEnv := flags.String("secret-env", "AI_TEAM_AUTH_SECRET", "Env с signing secret")
	flags.Parse(os.Args[2:])
	if strings.TrimSpace(*actorID) == "" || strings.TrimSpace(*roleValues) == "" {
		fatal("--actor и --roles обязательны")
	}
	roles, err := cloudidentity.ParseRoles(strings.Split(*roleValues, ","))
	if err != nil {
		fatal("%v", err)
	}
	principal, err := cloudidentity.NewPrincipal(*actorID, roles)
	if err != nil {
		fatal("%v", err)
	}
	manager, err := cloudidentity.NewTokenManager([]byte(os.Getenv(*secretEnv)))
	if err != nil {
		fatal("%v", err)
	}
	token, err := manager.Issue(principal, *ttl)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Println(token)
}

func cmdWorker() {
	flags := flag.NewFlagSet("worker", flag.ExitOnError)
	targetValue := flags.String("target", "", "Exact mounted repository target")
	dbPath := flags.String("db", "", "SQLite projection path")
	flags.Parse(os.Args[2:])
	if *targetValue == "" {
		fatal("worker требует --target")
	}
	target, err := absoluteTarget(*targetValue)
	if err != nil {
		fatal("Ошибка worker target: %v", err)
	}
	requireControlRoot(target)
	job, err := worker.DecodeJob(os.Stdin, target)
	if err != nil {
		fatal("Невалидный worker job: %v", err)
	}
	if *dbPath == "" {
		*dbPath = filepath.Join(target, ".ai-team", "web.db")
	} else if !filepath.IsAbs(*dbPath) {
		fatal("worker --db должен быть absolute path")
	}
	if err := safeio.RejectSymlink(*dbPath); err != nil {
		fatal("Небезопасный worker DB: %v", err)
	}
	recorderStore, err := webstore.New(*dbPath)
	if err != nil {
		fatal("Worker recorder: %v", err)
	}
	reg, err := newAgentRegistry(target)
	if err != nil {
		_ = recorderStore.Close()
		fatal("Worker agent registry: %v", err)
	}
	cfg := loadValidatedConfig(target, reg)
	if job.Operation != worker.OperationCancel {
		report := preflight.New(cfg, reg, target).Check(context.Background())
		if !report.Ready {
			_ = recorderStore.Close()
			printWorkerResult(job.RunID, worker.Result{
				SchemaVersion: worker.ResultSchemaVersion, RunID: job.RunID,
				Outcome: worker.OutcomeInfraFailed, Error: report.Error().Error(),
			})
			fatal("Worker preflight: %v", report.Error())
		}
	}
	engine := pipeline.NewRunEngine(pipeline.New(cfg, reg,
		pipeline.WithRecorder(web.NewStoreRecorder(recorderStore))))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result pipeline.RunResult
	switch job.Operation {
	case worker.OperationStart:
		result, err = engine.Start(ctx, job.RunConfig())
	case worker.OperationResume:
		result, err = engine.Resume(ctx, pipeline.ResumeConfig{
			RunID: job.RunID, TargetDir: target, ApproveGates: job.ApproveGates,
			ApprovePlanHash: job.ApprovePlanHash,
		})
	case worker.OperationCancel:
		result, err = engine.Cancel(pipeline.CancelConfig{RunID: job.RunID, TargetDir: target})
	}
	_ = recorderStore.Close()
	if err != nil {
		outcome := workerOutcomeFor(err)
		printWorkerResult(job.RunID, worker.Result{
			SchemaVersion: worker.ResultSchemaVersion, RunID: job.RunID,
			Outcome: outcome, Error: err.Error(),
		})
		fmt.Fprintf(os.Stderr, "worker %s остановлен: %v\n", job.RunID, err)
		os.Exit(exitCodeFor(err))
	}
	printWorkerResult(job.RunID, worker.Result{
		SchemaVersion: worker.ResultSchemaVersion, RunID: result.RunID,
		Outcome: string(result.Outcome),
	})
	logging.Printf("worker %s завершён: %s\n", result.RunID, result.Outcome)
}

func printWorkerResult(runID string, value worker.Result) {
	if runID != "" && value.RunID == "" {
		value.RunID = runID
	}
	if encoded, err := json.Marshal(value); err == nil {
		logging.Printf("%s%s\n", worker.ResultPrefix, encoded)
	}
}

// workerOutcomeFor переводит ошибку run в исход воркер-задачи. Ошибки вне
// контракта run (например, падение контроллера) считаются инфраструктурными.
func workerOutcomeFor(err error) string {
	var runErr *pipeline.RunError
	switch {
	case errors.As(err, &runErr):
		switch string(runErr.Outcome) {
		case "blocked":
			return worker.OutcomeBlocked
		case "stopped":
			return worker.OutcomeStopped
		case "canceled":
			return worker.OutcomeCanceled
		default:
			return worker.OutcomeFailed
		}
	case errors.Is(err, pipeline.ErrUserStopped):
		return worker.OutcomeStopped
	default:
		var approvalErr *pipeline.ApprovalRequiredError
		if errors.As(err, &approvalErr) {
			return worker.OutcomeWaitingApproval
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return worker.OutcomeCanceled
		}
		return worker.OutcomeInfraFailed
	}
}

func cmdSchedulerWorker() {
	flags := flag.NewFlagSet("scheduler-worker", flag.ExitOnError)
	targetValue := flags.String("target", ".", "Exact mounted repository target")
	schedulerDB := flags.String("scheduler-db", ".ai-team/scheduler.db", "Persistent scheduler SQLite")
	webDB := flags.String("db", ".ai-team/web.db", "SQLite event projection")
	artifactRoot := flags.String("artifact-store", ".ai-team/cloud-artifacts", "Persistent CAS root")
	workerCommand := flags.String("worker-command", "", "Executable ai-team worker")
	workerID := flags.String("worker-id", "", "Unique worker owner identity")
	once := flags.Bool("once", false, "Завершиться после одной попытки claim")
	pollInterval := flags.Duration("poll-interval", time.Second, "Интервал пустой очереди")
	leaseDuration := flags.Duration("lease", 30*time.Second, "Worker lease duration")
	maxConcurrent := flags.Int("max-concurrent", 4, "Global concurrency limit")
	perTarget := flags.Int("per-target", 1, "Concurrency limit одного target")
	flags.Parse(os.Args[2:])
	target, err := absoluteTarget(*targetValue)
	if err != nil {
		fatal("Scheduler worker target: %v", err)
	}
	requireControlRoot(target)
	if *workerCommand == "" {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			fatal("Scheduler worker executable: %v", executableErr)
		}
		*workerCommand = executable
	}
	for _, value := range []*string{schedulerDB, webDB, artifactRoot} {
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(target, *value)
		}
	}
	if *workerID == "" {
		hostname, _ := os.Hostname()
		*workerID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}
	queue, err := scheduler.Open(*schedulerDB, scheduler.Options{
		LeaseDuration: *leaseDuration, MaxConcurrent: *maxConcurrent, PerTarget: *perTarget,
	})
	if err != nil {
		fatal("Scheduler queue: %v", err)
	}
	defer queue.Close()
	processEngine, err := worker.NewProcessEngine([]string{*workerCommand}, target, *webDB)
	if err != nil {
		fatal("Scheduler ProcessEngine: %v", err)
	}
	cas, err := artifactstore.NewLocalCAS(*artifactRoot)
	if err != nil {
		fatal("Scheduler artifact store: %v", err)
	}
	archive, err := artifactstore.NewRunArchive(filepath.Join(target, ".ai-team", "runs"), cas)
	if err != nil {
		fatal("Scheduler archive: %v", err)
	}
	poller, err := scheduler.NewPoller(queue, processEngine, archive)
	if err != nil {
		fatal("Scheduler poller: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		claimed, pollErr := poller.RunOnce(ctx, *workerID)
		if pollErr != nil {
			if ctx.Err() != nil {
				return
			}
			fatal("Scheduler worker: %v", pollErr)
		}
		if *once {
			return
		}
		if !claimed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(*pollInterval):
			}
		}
	}
}

func cmdDecision() {
	flags := flag.NewFlagSet("decision", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	runID := flags.String("run", "", "Идентификатор run")
	approvalID := flags.String("approval", "", "Идентификатор approval")
	actorID := flags.String("actor", "", "Идентификатор человека")
	role := flags.String("role", "", "Роль человека")
	action := flags.String("action", "", "Выбранное действие")
	subject := flags.String("subject", "", "Точный SHA-256 subject")
	comment := flags.String("comment", "", "Комментарий к решению")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов decision: %v", err)
	}
	if flags.NArg() != 0 {
		fatal("Неожиданные аргументы decision: %s", strings.Join(flags.Args(), " "))
	}
	if *runID == "" || *approvalID == "" || *actorID == "" || *role == "" || *action == "" || *subject == "" {
		fatal("decision требует --run, --approval, --actor, --role, --action и --subject")
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	store, err := approval.NewStore(absolute)
	if err != nil {
		fatal("Ошибка approval store: %v", err)
	}
	value, err := store.Decide(*runID, *approvalID, approval.Decision{
		ActorID: *actorID, ActorRole: *role, Action: *action,
		SubjectHash: *subject, Comment: *comment,
	})
	if err != nil {
		fatal("Решение отклонено: %v", err)
	}
	if value.Status == approval.StatusResolved {
		logging.Printf("✓ Approval %s разрешён действием %s; продолжите: ai-team run --target %s --resume %s\n",
			value.ID, value.ResolvedAction, absolute, value.RunID)
		return
	}
	logging.Printf("✓ Решение записано для approval %s; ожидается quorum %s\n", value.ID, value.Quorum)
}

func validFeature(name string) bool {
	return workflow.ValidFeature(name)
}

func absoluteTarget(target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("не удалось определить абсолютный target path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func agentsFS() fs.FS {
	s, err := fs.Sub(agentdata.Agents, "agents")
	if err != nil {
		return agentdata.Agents
	}
	return s
}

func newAgentRegistry(target string) (*agent.Registry, error) {
	projectAgents := filepath.Join(target, ".ai-team", "agents")
	if err := safeio.ValidateTree(projectAgents); err != nil {
		return nil, err
	}
	layers := []agent.Layer{{Name: "project", FS: os.DirFS(filepath.Join(target, ".ai-team", "agents"))}}
	for index, pluginDir := range filepath.SplitList(os.Getenv("AI_TEAM_AGENT_PATH")) {
		if pluginDir == "" {
			continue
		}
		if absolute, err := filepath.Abs(pluginDir); err == nil {
			pluginDir = absolute
		}
		layers = append(layers, agent.Layer{Name: fmt.Sprintf("plugin-%d:%s", index, pluginDir), FS: os.DirFS(pluginDir)})
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		userDir := filepath.Join(configDir, "ai-team", "agents")
		layers = append(layers, agent.Layer{Name: "user:" + userDir, FS: os.DirFS(userDir)})
	}
	layers = append(layers, agent.Layer{Name: "builtin", FS: agentsFS()})
	return agent.NewLayered(layers...), nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// applyOutputMode сканирует аргументы на --json/--quiet и переключает
// machine-readable режим вывода (OPS-6) ДО диспатча команды. Глобальные
// флаги вывода при этом вырезаются из os.Args, чтобы подкоманды (их
// собственные FlagSet'ы) никогда их не видели и не падали на неизвестном
// флаге. Работает и до, и после имени подкоманды (сканируется весь хвост,
// начиная с индекса 1).
func applyOutputMode() {
	filtered := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--json":
			logging.SetMode(logging.ModeJSON)
		case "--quiet", "-q":
			logging.SetMode(logging.ModeQuiet)
		default:
			filtered = append(filtered, os.Args[i])
		}
	}
	if len(filtered) != len(os.Args) {
		os.Args = filtered
	}
}

// checkControlRoot проверяет, что `.ai-team` существует и безопасен (не
// symlink и не файл), и различает эти два разных случая при отказе: "проект
// не инициализирован" — это не то же самое, что "инициализирован, но
// небезопасен", и пользователю нужно разное действие в ответ на каждый.
func checkControlRoot(target string) error {
	if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("проект не инициализирован в %s — сначала выполните `ai-team init`", target)
		}
		return fmt.Errorf("небезопасный control root: %w", err)
	}
	return nil
}

func requireControlRoot(target string) {
	if err := checkControlRoot(target); err != nil {
		fatal("%v", err)
	}
}

func cmdInit() {
	initFlags := flag.NewFlagSet("init", flag.ExitOnError)
	targetFlag := initFlags.String("target", ".", "Путь к целевому проекту")
	profileFlag := initFlags.String("profile", config.ProfileStandard, "Профиль workflow: fast, standard или regulated")
	writeGitignore := initFlags.Bool("write-gitignore", false, "Записать разделяемое правило в .gitignore вместо локального Git exclude")
	if err := initFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов init: %v", err)
	}
	if initFlags.NArg() != 0 {
		fatal("Неожиданные аргументы init: %s", strings.Join(initFlags.Args(), " "))
	}
	target := *targetFlag
	var err error
	target, err = absoluteTarget(target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	if _, err := safeio.EnsureDir(target, ".ai-team"); err != nil {
		fatal("Небезопасный control root: %v", err)
	}

	dirs := [][]string{
		{".ai-team", "artifacts", "tasks"},
		{".ai-team", "reports"},
		{".ai-team", "logs"},
	}
	for _, components := range dirs {
		if _, err := safeio.EnsureDir(target, components...); err != nil {
			fatal("Ошибка создания %s: %v", filepath.Join(components...), err)
		}
	}

	cfg, err := config.DefaultProfile(*profileFlag)
	if err != nil {
		fatal("Ошибка профиля: %v", err)
	}
	switch profile, warning := cfg.ApplyDetectedChecks(target); {
	case warning != "":
		fmt.Fprintf(os.Stderr, "Предупреждение: %s\n", warning)
	case profile != "":
		logging.Printf("✓ Обнаружен verification profile: %s\n", profile)
	default:
		fmt.Fprintln(os.Stderr, "Предупреждение: тестовый профиль не обнаружен; delivery будет запрещён до настройки required unit/integration/e2e check")
	}
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		data, err := cfg.Marshal()
		if err != nil {
			fatal("Ошибка сериализации конфига: %v", err)
		}
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			fatal("Ошибка создания конфига: %v", err)
		}
	}

	if err := runtime.CheckCLI(cfg.CLI); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: %v\n", err)
	}

	if *profileFlag == config.ProfileFast {
		if err := writeFastReviewerOverride(target); err != nil {
			fatal("Ошибка project-local override для fast-профиля: %v", err)
		}
		logging.Printf("✓ fast-профиль: reviewer совмещает ревью и верификацию (.ai-team/agents/reviewer/)\n")
	}

	ignorePath, err := ensureControlIgnored(target, *writeGitignore)
	if err != nil {
		fatal("Ошибка настройки ignore policy: %v", err)
	}
	if ignorePath != "" {
		logging.Printf("✓ .ai-team/ исключён через %s\n", ignorePath)
	}

	logging.Printf("✓ .ai-team/ инициализирован в %s\n", target)
}

// writeFastReviewerOverride создаёт project-local определение reviewer'а,
// которое совмещает ревью и верификацию (output verification с тем же
// verdict-маркером) — deployer precondition остаётся выполнимым без
// отдельной стадии verifier. Prompt наследует встроенный и дополняется
// секцией про верификацию.
func writeFastReviewerOverride(target string) error {
	embedded, err := fs.Sub(agentdata.Agents, "agents/reviewer")
	if err != nil {
		return err
	}
	basePrompt, err := fs.ReadFile(embedded, "prompt.md")
	if err != nil {
		return fmt.Errorf("встроенный prompt reviewer: %w", err)
	}
	overrideDir := filepath.Join(target, ".ai-team", "agents", "reviewer")
	if _, err := safeio.EnsureDir(target, ".ai-team", "agents", "reviewer"); err != nil {
		return err
	}
	def := `name: reviewer
description: Reviewer (fast) — ревью кода и верификация одним проходом
runtime: agentcli
cli: opencode
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  specs: '{feature}/specs'
  test-report: '{feature}/test-report.md'
  candidate: '{feature}/.control/review-candidate.json'
outputs:
  review: '{feature}/review.md'
  verification: '{feature}/verification.md'
`
	prompt := strings.TrimSpace(string(basePrompt)) + `

## Верификация (fast profile)

Дополнительно к review.md подготовь verification.md — итог самопроверки
реализации перед доставкой: соответствие acceptance criteria из proposal,
результаты ручной проверки ключевых сценариев, известные ограничения и
непроверенные сценарии. Заверши файл тем же маркером вердикта:

**Verdict:** APPROVED | CHANGES_REQUESTED | REJECTED
`
	defPath := filepath.Join(overrideDir, "def.yaml")
	if err := os.WriteFile(defPath, []byte(def), 0644); err != nil {
		return err
	}
	promptPath := filepath.Join(overrideDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt+"\n"), 0644); err != nil {
		return err
	}
	return nil
}

// ensureControlIgnored гарантирует исключение .ai-team/ из Git. По умолчанию
// используется локальный info/exclude, чтобы init не изменял workspace.
func ensureControlIgnored(target string, writeGitignore bool) (string, error) {
	if writeGitignore {
		path := filepath.Join(target, ".gitignore")
		return path, appendIgnoreRule(path)
	}

	check := exec.Command("git", "-C", target, "rev-parse", "--is-inside-work-tree")
	if output, err := check.Output(); err != nil || strings.TrimSpace(string(output)) != "true" {
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return "", fmt.Errorf("не удалось проверить Git repository: %w", err)
		}
		return "", nil
	}

	command := exec.Command("git", "-C", target, "rev-parse", "--git-path", "info/exclude")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("не удалось определить Git exclude path: %w", err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", errors.New("Git вернул пустой exclude path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(target, path)
	}
	path = filepath.Clean(path)
	return path, appendIgnoreRule(path)
}

func appendIgnoreRule(path string) error {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ".ai-team/" {
			return nil
		}
	}

	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(prefix + "# ai-team\n.ai-team/\n"); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func loadValidatedConfig(target string, reg *agent.Registry) *config.Config {
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if err := safeio.RejectSymlink(cfgPath); err != nil {
		fatal("Небезопасный config path: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("Ошибка загрузки конфига: %v", err)
	}
	if err := cfg.Validate(reg); err != nil {
		fatal("%v", err)
	}
	return cfg
}

func cmdRun() {
	runFlags := flag.NewFlagSet("run", flag.ExitOnError)
	feature := runFlags.String("feature", "", "Имя фичи")
	taskDesc := runFlags.String("task", "", "Описание задачи")
	target := runFlags.String("target", ".", "Путь к целевому проекту")
	resumeRunID := runFlags.String("resume", "", "Продолжить non-terminal run")
	approveGates := runFlags.Bool("approve-gates", false, "Подтвердить gate-точки в non-interactive режиме (forward-гейты default-профилей отложены до delivery-решения, APF-1)")
	approvePlan := runFlags.String("approve-plan", "", "SHA-256 ранее показанного delivery plan (ratify-ит отложенные гейты run'а)")

	runFlags.Parse(os.Args[2:])
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absTarget
	requireControlRoot(*target)

	if *resumeRunID == "" {
		if *feature == "" {
			fatal("Укажите --feature")
		}
		if !validFeature(*feature) {
			fatal("недопустимое имя фичи: %q (допустимы буквы, цифры, \"-\", \"_\", \".\")", *feature)
		}
	} else if *feature != "" || *taskDesc != "" {
		fatal("--resume нельзя сочетать с --feature или --task; identity и next stage загружаются из state")
	}

	// Config/registry validation is intentionally before task.md writes: a bad
	// control-plane definition must fail without mutating the target workspace.
	reg, err := newAgentRegistry(*target)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}
	cfg := loadValidatedConfig(*target, reg)

	if *resumeRunID != "" {
		// Resume загружает task/feature после получения workspace lock.
	} else {
		if *taskDesc == "" {
			fatal("Укажите --task")
		}
		warnIfAlreadyDelivered(*target, *feature)
	}

	opts := []pipeline.Option{}
	if recorder, closeStore := openRecorder(*target); recorder != nil {
		opts = append(opts, pipeline.WithRecorder(recorder))
		defer closeStore()
	}

	p := pipeline.New(cfg, reg, opts...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine := pipeline.NewRunEngine(p)
	containmentProfile := "trusted-local"
	if cfg.Containment != nil && cfg.Containment.Profile != "" {
		containmentProfile = cfg.Containment.Profile
	}
	var runResult pipeline.RunResult
	if *resumeRunID != "" {
		runResult, err = engine.Resume(ctx, pipeline.ResumeConfig{
			RunID: *resumeRunID, TargetDir: *target, ApproveGates: *approveGates, ApprovePlanHash: *approvePlan,
		})
	} else {
		runResult, err = engine.Start(ctx, pipeline.RunConfig{
			Feature: *feature, TaskDesc: *taskDesc, TargetDir: *target,
			ApproveGates: *approveGates, ApprovePlanHash: *approvePlan,
			ContainmentProfile: containmentProfile,
		})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s Пайплайн остановлен: %v\n", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(exitCodeFor(err))
	}

	if string(runResult.Outcome) == "completed_with_warnings" {
		logging.Printf("\n%s Пайплайн выполнен с предупреждениями\n", ui.Colorize("!", ui.ColorYellow))
	} else {
		logging.Printf("\n%s Пайплайн выполнен\n", ui.Colorize("✓", ui.ColorGreen))
	}
	if logging.GetMode() == logging.ModeJSON || logging.GetMode() == logging.ModeQuiet {
		logging.Emit(logging.Record{
			Level: "ok", Command: "run", Type: "run",
			Message: "Пайплайн выполнен",
			Data: map[string]any{
				"outcome": string(runResult.Outcome),
				"run_id":  runResult.RunID,
				"feature": *feature,
			},
			Exit: exitOK,
		})
	}
}

func exitCodeFor(err error) int {
	var runErr *pipeline.RunError
	var blocked *pipeline.BlockedError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &runErr):
		switch string(runErr.Outcome) {
		case "blocked":
			return exitBlocked
		case "stopped":
			return exitUserStopped
		default:
			return exitFailed
		}
	case errors.As(err, &blocked):
		return exitBlocked
	case errors.Is(err, pipeline.ErrUserStopped):
		return exitUserStopped
	default:
		return exitFailed
	}
}

// warnIfAlreadyDelivered предупреждает, если --feature уже был доведён до
// успешной delivery в прошлом run. Это только диагностика: она не блокирует
// новый run — фича могла осознанно получить повторную порцию работы под тем
// же именем. Без этого предупреждения повторный run на уже доставленной
// фиче тихо перезаписывает artifacts и падает на coder с сообщением "агент
// не создал изменений", которое вне контекста читается как баг агента.
func warnIfAlreadyDelivered(target, feature string) {
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	delivered, ok, err := evidence.FindDelivered(runsRoot, feature)
	if err != nil || !ok {
		return
	}
	ref := delivered.Delivery.PRURL
	if ref == "" {
		ref = delivered.Delivery.CommitSHA
	}
	fmt.Fprintf(os.Stderr, "%s Фича %q уже была доставлена ранее (run %s, %s). Продолжаю новый run с тем же именем — предыдущая поставка не будет затронута.\n",
		ui.Colorize("⚠", ui.ColorYellow), feature, delivered.RunID, ref)
}

// openRecorder открывает SQLite-store для записи запусков (web-дашборд).
// Недоступность БД не мешает запуску.
func openRecorder(target string) (pipeline.Recorder, func()) {
	dbPath := filepath.Join(target, ".ai-team", "web.db")
	if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	if err := safeio.RejectSymlink(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	s, err := webstore.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	return web.NewStoreRecorder(s), func() { s.Close() }
}

func cmdEval() {
	evalFlags := flag.NewFlagSet("eval", flag.ExitOnError)
	agentName := evalFlags.String("agent", "", "Имя агента для оценки")
	artifactPath := evalFlags.String("artifact", "", "Путь к артефакту для оценки")
	feature := evalFlags.String("feature", "", "Запустить одного агента и оценить")
	taskDesc := evalFlags.String("task", "", "Описание задачи")
	target := evalFlags.String("target", ".", "Путь к проекту")
	samples := evalFlags.Int("samples", 1, "Число независимых LLM-оценок (1-20)")
	jsonOut := evalFlags.String("json-out", "", "Путь JSON evidence")

	evalFlags.Parse(os.Args[2:])
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absTarget
	requireControlRoot(*target)
	if *samples < 1 || *samples > 20 {
		fatal("--samples должен быть от 1 до 20")
	}
	if *jsonOut == "" && *agentName != "" {
		*jsonOut = defaultEvalOutput(*target, *agentName)
	} else if *jsonOut != "" && !filepath.IsAbs(*jsonOut) {
		*jsonOut = filepath.Join(*target, *jsonOut)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *artifactPath != "" && *agentName != "" {
		if err := eval.RunAndPrintQuality(ctx, *agentName, *artifactPath, nil, *target, *samples, *jsonOut); err != nil {
			fatal("Ошибка оценки: %v", err)
		}
		return
	}

	if *feature != "" && *taskDesc != "" && *agentName != "" {
		if !validFeature(*feature) {
			fatal("недопустимое имя фичи: %q", *feature)
		}
		if err := evalSingleAgent(ctx, *target, *feature, *taskDesc, *agentName, *samples, *jsonOut); err != nil {
			fatal("Ошибка оценки: %v", err)
		}
		return
	}

	fatal("Укажите --artifact + --agent, либо --feature + --task + --agent")
}

// evalSingleAgent запускает пайплайн из одного агента и оценивает его
// фактические выходные артефакты (пути из def.yaml).
func evalSingleAgent(ctx context.Context, target, feature, taskDesc, agentName string, samples int, outputPath string) error {
	reg, err := newAgentRegistry(target)
	if err != nil {
		return err
	}
	a, err := reg.Load(agentName)
	if err != nil {
		return err
	}

	base := loadValidatedConfig(target, reg)
	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{{Name: agentName}},
		CLI:            base.CLI,
		Model:          base.Model,
		Effort:         base.Effort,
		StageTimeout:   base.StageTimeout,
	}

	p := pipeline.New(cfg, reg)
	if err := p.Run(ctx, pipeline.RunConfig{Feature: feature, TaskDesc: taskDesc, TargetDir: target}); err != nil {
		return fmt.Errorf("пайплайн упал: %w", err)
	}

	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	evaluated := 0
	for outputName, outPath := range a.Outputs {
		fullPath := filepath.Join(artifactRoot, runtime.ReplaceVars(outPath, feature))
		if info, err := os.Stat(fullPath); err != nil || info.IsDir() {
			continue
		}
		logging.Printf("\n--- Оценка артефакта: %s ---\n", fullPath)
		artifactOutput := outputPath
		if len(a.Outputs) > 1 {
			extension := filepath.Ext(outputPath)
			safeOutputName := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(outputName, "-")
			artifactOutput = strings.TrimSuffix(outputPath, extension) + "-" + safeOutputName + extension
		}
		if err := eval.RunAndPrintQuality(ctx, agentName, fullPath, nil, target, samples, artifactOutput); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка оценки %s: %v\n", fullPath, err)
		} else {
			evaluated++
		}
	}
	if evaluated == 0 {
		return fmt.Errorf("не найдено артефактов для оценки у агента %s", agentName)
	}
	return nil
}

func defaultEvalOutput(target, agentName string) string {
	safeAgent := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(agentName, "-")
	return filepath.Join(target, ".ai-team", "evals", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+safeAgent+".json")
}

// cmdUsage печатает usage-сводку завершённого run из {target}/.ai-team/runs/<run_id>/usage.json.
func cmdUsage() {
	flags := flag.NewFlagSet("usage", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов usage: %v", err)
	}
	if flags.NArg() != 1 {
		fatal("Использование: ai-team usage --target <dir> <run_id>")
	}
	runID := flags.Arg(0)
	if runID == "." || runID == ".." || filepath.Base(runID) != runID {
		fatal("Некорректный run_id: %q", runID)
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	path := filepath.Join(absolute, ".ai-team", "runs", runID, "usage.json")
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil {
		fatal("Не удалось прочитать usage: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope metrics.UsageEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		fatal("Повреждённый usage.json: %v", err)
	}
	logging.Printf("Run:      %s\n", envelope.RunID)
	logging.Printf("Feature:  %s\n", envelope.Feature)
	logging.Printf("Outcome:  %s\n", envelope.Outcome)
	logging.Printf("Период:   %s → %s\n",
		envelope.StartedAt.Format(time.RFC3339), envelope.FinishedAt.Format(time.RFC3339))
	logging.Printf("Loopback: %d\n", envelope.LoopbackCycles)
	tokens := "unknown"
	if !envelope.TokensUnknown {
		tokens = "known"
	}
	logging.Printf("Токены:   %s\n\n", tokens)
	// Containment receipt (V0-P1-4) — если присутствует.
	if cdata, cerr := safeio.ReadRegularFile(filepath.Join(absolute, ".ai-team", "runs", runID, "containment.json"), 1<<20); cerr == nil {
		var receipt containment.Receipt
		if err := json.Unmarshal(cdata, &receipt); err == nil {
			logging.Printf("Containment (%s):\n", receipt.Profile)
			for _, axis := range []containment.Axis{containment.AxisFS, containment.AxisNet, containment.AxisProc, containment.AxisEnv} {
				logging.Printf("  %-5s %s\n", axis, receipt.Axes[axis])
			}
			fmt.Println()
		}
	}
	if err := envelope.Format(os.Stdout); err != nil {
		fatal("Ошибка вывода usage: %v", err)
	}
}

func cmdList() {
	target, err := absoluteTarget(".")
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(target, ".ai-team")); statErr == nil {
		if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
			fatal("Небезопасный control root: %v", err)
		}
	} else if !os.IsNotExist(statErr) {
		fatal("Ошибка control root: %v", statErr)
	}
	reg, err := newAgentRegistry(target)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}

	logging.Printf("%-20s %-15s %-10s %-20s %s\n", "Имя", "Runtime", "CLI", "Источник", "Описание")
	fmt.Println(strings.Repeat("-", 80))
	agents, failures := reg.List()
	for _, a := range agents {
		logging.Printf("%-20s %-15s %-10s %-20s %s\n", a.Name, a.RuntimeType, a.CLI, a.Source, a.Description)
	}
	for _, f := range failures {
		fmt.Fprintf(os.Stderr, "%s агент %q не загружен: %v\n", ui.Colorize("⚠", ui.ColorYellow), f.Name, f.Err)
	}
}

// cmdVerify проверяет tamper-evident evidence терминального run или
// самодостаточного portable bundle (V0-4).
//
//	ai-team verify --target <dir> <run_id>   — локальная evidence run
//	ai-team verify <bundle-dir>              — bundle без repo и .ai-team
func cmdVerify() {
	verifyFlags := flag.NewFlagSet("verify", flag.ExitOnError)
	target := verifyFlags.String("target", ".", "Путь к целевому проекту")
	verifyKey := verifyFlags.String("verify-key", "", "Путь к ed25519 public key (PEM или raw) для DSSE-верификации подписи bundle (P1-5)")
	if err := verifyFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов verify: %v", err)
	}
	if verifyFlags.NArg() != 1 {
		fatal("Использование: ai-team verify [--target <dir>] [--verify-key <path>] <run_id> | <bundle-dir>")
	}
	arg := verifyFlags.Arg(0)
	if len(arg) > 1024 {
		fatal("недопустимый аргумент verify")
	}
	var keyVerify ed25519.PublicKey
	if *verifyKey != "" {
		key, err := dsse.LoadPublicKey(*verifyKey)
		if err != nil {
			fatal("Ошибка загрузки verify key: %v", err)
		}
		keyVerify = key
	}
	info, statErr := os.Stat(arg)
	if statErr == nil && info.IsDir() {
		if gateBundleExists(arg) {
			digest, err := gate.VerifyBundle(arg, keyVerify)
			if err != nil {
				fmt.Fprintf(os.Stderr, "✗ Gate bundle %s: %v\n", arg, ui.Colorize(err.Error(), ui.ColorRed))
				logging.Emit(logging.Record{Level: "error", Command: "verify", Type: "gate_bundle", Message: err.Error(), Exit: exitFailed})
				os.Exit(exitFailed)
			}
			logging.Printf("✓ Gate bundle %s: OK — records согласованы, bundle_sha256 %s%s\n", arg, digest, sigNote(keyVerify))
			logging.Emit(logging.Record{Level: "ok", Command: "verify", Type: "gate_bundle", Message: "Gate bundle OK",
				Data: map[string]any{"bundle_sha256": digest}, Exit: exitOK})
			return
		}
		if err := export.VerifyBundle(arg, keyVerify); err != nil {
			fmt.Fprintf(os.Stderr, "✗ Bundle %s: %v\n", arg, ui.Colorize(err.Error(), ui.ColorRed))
			logging.Emit(logging.Record{Level: "error", Command: "verify", Type: "run_bundle", Message: err.Error(), Exit: exitFailed})
			os.Exit(exitFailed)
		}
		logging.Printf("✓ Bundle %s: OK — records, event chain, anchor, attempt manifests и attestation v1 согласованы%s\n", arg, sigNote(keyVerify))
		logging.Emit(logging.Record{Level: "ok", Command: "verify", Type: "run_bundle",
			Message: "Bundle OK", Data: map[string]any{"target": arg}, Exit: exitOK})
		return
	}
	if strings.ContainsAny(arg, `/\`) || arg == "." || arg == ".." || filepath.Base(arg) != arg {
		if statErr != nil && !os.IsNotExist(statErr) {
			fatal("Ошибка доступа к %q: %v", arg, statErr)
		}
		fatal("Bundle %q не найден (каталог bundle должен существовать)", arg)
	}
	runID := arg
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	runDir := filepath.Join(absolute, ".ai-team", "runs", runID)
	if len(keyVerify) > 0 {
		fatal("Run %s: DSSE-подпись применима только к bundle (export/gate), а не к live run evidence; --verify-key не используется на этой ветке — отказ (fail-closed)", runID)
	}
	if err := export.VerifyEvidence(runDir); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Run %s: %v\n", runID, ui.Colorize(err.Error(), ui.ColorRed))
		logging.Emit(logging.Record{Level: "error", Command: "verify", Type: "run", Message: err.Error(), Exit: exitFailed})
		os.Exit(exitFailed)
	}
	logging.Printf("✓ Run %s: anchor OK — event chain, manifests digest, attempt manifests и attestation v1 согласованы\n", runID)
	logging.Emit(logging.Record{Level: "ok", Command: "verify", Type: "run",
		Message: "Run OK", Data: map[string]any{"run_id": runID}, Exit: exitOK})
}

// gateBundleExists отличает gate attestation bundle (V0-5) от run bundle (V0-4).
func gateBundleExists(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "gate.json"))
	return err == nil && !info.IsDir()
}

// sigNote возвращает постфикс о статусе подписи в выводе verify.
func sigNote(key ed25519.PublicKey) string {
	if len(key) == 0 {
		return " (без верификации подписи: --verify-key не задан)"
	}
	return " — подпись DSSE ed25519 подтверждена"
}

func cmdWeb() {
	webFlags := flag.NewFlagSet("web", flag.ExitOnError)
	port := webFlags.String("port", "8080", "Port for web server")
	host := webFlags.String("host", "127.0.0.1", "Bind host")
	dbPath := webFlags.String("db", ".ai-team/web.db", "Path to SQLite database")
	distDir := webFlags.String("dist", "web/dist", "Path to frontend dist directory")
	artifacts := webFlags.String("artifacts", ".ai-team/artifacts", "Artifact root directory")
	targetFlag := webFlags.String("target", ".", "Путь к целевому проекту")
	authSecretEnv := webFlags.String("auth-secret-env", "AI_TEAM_AUTH_SECRET", "Env с cloud auth HMAC secret")
	workerCommand := webFlags.String("worker-command", "", "Executable ai-team для disposable worker process")
	schedulerDB := webFlags.String("scheduler-db", "", "Persistent scheduler SQLite (включает enqueue-only mode)")
	schedulerMax := webFlags.Int("max-concurrent", 4, "Scheduler global concurrency")
	schedulerPerTarget := webFlags.Int("per-target", 1, "Scheduler per-target concurrency")
	webFlags.Parse(os.Args[2:])
	target, err := absoluteTarget(*targetFlag)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(target)
	if !filepath.IsAbs(*dbPath) {
		*dbPath = filepath.Join(target, *dbPath)
	}
	if !filepath.IsAbs(*artifacts) {
		*artifacts = filepath.Join(target, *artifacts)
	}
	if *schedulerDB != "" && !filepath.IsAbs(*schedulerDB) {
		*schedulerDB = filepath.Join(target, *schedulerDB)
	}
	if filepath.Clean(*dbPath) == filepath.Join(target, ".ai-team", "web.db") {
		if err := safeio.RejectSymlink(*dbPath); err != nil {
			fatal("Небезопасный путь БД: %v", err)
		}
	}

	if dir := filepath.Dir(*dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fatal("Ошибка создания каталога БД: %v", err)
		}
	}

	reg, err := newAgentRegistry(target)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}
	cfg := loadValidatedConfig(target, reg)
	recorderStore, err := webstore.New(*dbPath)
	if err != nil {
		fatal("Ошибка recorder store: %v", err)
	}
	defer recorderStore.Close()
	localEngine := pipeline.NewRunEngine(pipeline.New(cfg, reg,
		pipeline.WithRecorder(web.NewStoreRecorder(recorderStore))))
	var runController *control.Controller
	var schedulerQueue *scheduler.Queue
	if *schedulerDB != "" {
		if *workerCommand != "" {
			fatal("--scheduler-db и --worker-command нельзя использовать одновременно: scheduler worker запускается отдельно")
		}
		schedulerQueue, err = scheduler.Open(*schedulerDB, scheduler.Options{
			MaxConcurrent: *schedulerMax, PerTarget: *schedulerPerTarget,
		})
		if err != nil {
			fatal("Ошибка scheduler queue: %v", err)
		}
		defer schedulerQueue.Close()
		queueEngine, queueErr := scheduler.NewQueueEngine(schedulerQueue, target)
		if queueErr != nil {
			fatal("Ошибка queue engine: %v", queueErr)
		}
		runController, err = control.New(queueEngine, target)
	} else if *workerCommand != "" {
		processEngine, processErr := worker.NewProcessEngine([]string{*workerCommand}, target, *dbPath)
		if processErr != nil {
			fatal("Ошибка worker launcher: %v", processErr)
		}
		runController, err = control.New(processEngine, target,
			control.WithPreflight(preflight.New(cfg, reg, target)))
	} else {
		runController, err = control.New(localEngine, target,
			control.WithPreflight(preflight.New(cfg, reg, target)))
	}
	if err != nil {
		fatal("Ошибка run controller: %v", err)
	}
	serverOptions := []web.ServerOption{web.WithRunController(runController)}
	authEnabled := false
	if secret := os.Getenv(*authSecretEnv); secret != "" {
		tokenManager, managerErr := cloudidentity.NewTokenManager([]byte(secret))
		if managerErr != nil {
			fatal("Ошибка cloud authentication: %v", managerErr)
		}
		serverOptions = append(serverOptions, web.WithAuthenticator(tokenManager))
		authEnabled = true
	}
	srv, err := web.NewServer(*dbPath, *distDir, *artifacts, serverOptions...)
	if err != nil {
		fatal("Ошибка запуска web сервера: %v", err)
	}
	defer srv.Close()
	// Фоновые ошибки run не должны исчезать после 202: фиксируем их в
	// SQLite projection дашборда.
	runController.SetFailureSink(srv.RecordAdmissionFailure)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	addr := net.JoinHostPort(*host, *port)
	if !authEnabled && *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
		fatal("web UI не имеет authentication и может bind только loopback host")
	}
	logging.Printf("Web UI available at http://%s\n", addr)
	if err := srv.ListenAndServe(addr); err != nil {
		fatal("Ошибка сервера: %v", err)
	}
}
