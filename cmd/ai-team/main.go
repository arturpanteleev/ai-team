package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/ciimport"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
	"github.com/arturpanteleev/ai-team/pkg/eval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/export"
	"github.com/arturpanteleev/ai-team/pkg/gate"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
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
	case "ci-import":
		cmdCIImport()
	case "redact":
		cmdRedact()
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
	case "db":
		cmdDB()
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
  ai-team list [--target <path>]   Список доступных агентов
  ai-team ci-import                Импортировать объяснимый набор checks из project CI
  ai-team usage <run_id>           Токены и приблизительная доля подписки завершённого run
  ai-team redact verify|scan|redact   P1-6 redaction-контракт: сеcrets-скан evidence,
                                   verify (fail-closed для экспорта) или detached-копия
                                   с заменой секретов на [REDACTED:...]
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
  ai-team db backup --db <path> --out <path>
                                   Снимок SQLite controller database
  ai-team db restore --from <snapshot> --out <new-path>
                                   Восстановить SQLite snapshot в новый путь
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
  --db <path>               SQLite web DB для очистки approvals старых terminal runs
  --older-than <duration>   Возраст terminal-ранов для уборки state (по умолчанию 720h)
  --keep-last <n>           Защитить n самых свежих terminal-ранов (по умолчанию 20)
  --dry-run                 Только показать план: что удалится и сколько байт
  --prune-runs              Разрешить удаление immutable run evidence (.ai-team/runs),
                            но только для run с verified-записью state/exports (V0-4 guard;
                            пока нет экспорта — флаг безопасно не удаляет evidence)

Флаги db backup:
  --db <path>               Существующая SQLite controller database
  --out <path>              Новый файл snapshot (существующий файл не перезаписывается)
Флаги db restore:
  --from <path>             SQLite snapshot, прошедший integrity check
  --out <path>              Новый путь для восстановленной базы
  Обе команды затрагивают только SQLite database; run evidence, artifacts и
  другие файлы состояния нужно резервировать отдельно.

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
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = flags.Parse(os.Args[2:])
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
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = flags.Parse(os.Args[2:])
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
	if err := worker.ProtectWorkerProcess(); err != nil {
		fatal("Worker process protection: %v", err)
	}
	_, apiAddressSet := os.LookupEnv(worker.WorkerAPIAddressEnv)
	_, apiSocketSet := os.LookupEnv(worker.WorkerAPISocketEnv)
	_, apiTokenSet := os.LookupEnv(worker.WorkerAPITokenEnv)
	egressSocket, egressSocketSet := os.LookupEnv(worker.OpenAIEgressSocketEnv)
	egressToken, egressTokenSet := os.LookupEnv(worker.OpenAIEgressTokenEnv)
	controllerAPI := apiAddressSet || apiSocketSet || apiTokenSet
	if egressSocketSet != egressTokenSet {
		fatal("worker OpenAI egress требует одновременно Unix socket и capability")
	}
	if egressSocketSet && !controllerAPI {
		fatal("worker OpenAI egress разрешён только в controller API mode")
	}
	if controllerAPI && *dbPath != "" {
		fatal("worker controller API mode rejects --db")
	}
	var recorderStore *webstore.Store
	var approvalStore pipeline.ApprovalStore
	var businessBriefStore pipeline.BriefStore
	var candidateMetadataStore candidate.MetadataStore
	var usageEnvelopeWriter pipeline.UsageEnvelopeWriter
	var terminalRecordWriter pipeline.TerminalRecordWriter
	var attestationWriter pipeline.AttestationWriter
	var containmentReceiptWriter pipeline.ContainmentReceiptWriter
	var candidateEvidenceStore pipeline.CandidateEvidenceStore
	var attemptManifestSource evidence.AttemptManifestSource
	var attemptManifestWriter pipeline.AttemptManifestWriter
	var eventLogSource evidence.EventLog
	var recorder pipeline.Recorder
	var lifecycleStore lifecycle.StorePort
	if controllerAPI {
		apiPort, apiErr := worker.NewWorkerAPIPort(job)
		if apiErr != nil {
			fatal("Worker controller API: %v", apiErr)
		}
		recorder = worker.NewWorkerAPIRecorder(apiPort)
		approvalStore = worker.NewWorkerAPIApprovals(apiPort)
		businessBriefStore = worker.NewWorkerAPIBriefs(apiPort)
		candidateMetadataStore = worker.NewWorkerAPICandidates(apiPort)
		if apiPort.SupportsControllerUsageStore() {
			usageEnvelopeWriter = worker.NewWorkerAPIUsageEnvelopeWriter(apiPort)
			terminalRecordWriter = worker.NewWorkerAPITerminalRecordWriter(apiPort)
			attestationWriter = worker.NewWorkerAPIAttestationWriter(apiPort)
			containmentReceiptWriter = worker.NewWorkerAPIContainmentReceiptWriter(apiPort)
			candidateEvidenceStore = worker.NewWorkerAPICandidateEvidenceStore(apiPort)
			manifestStore := worker.NewWorkerAPIAttemptManifestStore(apiPort)
			attemptManifestSource = manifestStore
			attemptManifestWriter = manifestStore
			eventLogSource = worker.NewWorkerAPIEventLog(apiPort)
		}
		lifecycleStore = worker.NewWorkerAPILifecycle(apiPort)
	} else {
		if *dbPath == "" {
			*dbPath = filepath.Join(target, ".ai-team", "web.db")
		} else if !filepath.IsAbs(*dbPath) {
			fatal("worker --db должен быть absolute path")
		}
		if err := safeio.RejectSymlink(*dbPath); err != nil {
			fatal("Небезопасный worker DB: %v", err)
		}
		recorderStore, err = webstore.New(*dbPath)
		if err != nil {
			fatal("Worker recorder: %v", err)
		}
		localApprovalStore, storeErr := approval.NewSQLiteStore(*dbPath)
		if storeErr != nil {
			_ = recorderStore.Close()
			fatal("Worker approval store: %v", storeErr)
		}
		defer func() { _ = localApprovalStore.Close() }()
		approvalStore = approval.NewWorkerStore(localApprovalStore)
		recorder = web.NewStoreRecorder(recorderStore)
	}
	if err := worker.ClearWorkerProcessCapabilities(); err != nil {
		fatal("Worker capability environment: %v", err)
	}
	registryPaths, hasRegistrySnapshot, registryErr := worker.AgentRegistryPathsFromEnvironment()
	if registryErr != nil {
		if recorderStore != nil {
			_ = recorderStore.Close()
		}
		fatal("Worker agent registry paths: %v", registryErr)
	}
	var reg *agent.Registry
	if hasRegistrySnapshot {
		reg, err = newAgentRegistryWithPaths(target, registryPaths)
	} else {
		reg, err = newAgentRegistry(target)
	}
	if err != nil {
		if recorderStore != nil {
			_ = recorderStore.Close()
		}
		fatal("Worker agent registry: %v", err)
	}
	cfg := loadValidatedConfig(target, reg)
	if job.Operation != worker.OperationCancel {
		report := preflight.New(cfg, reg, target).Check(context.Background())
		if !report.Ready {
			if recorderStore != nil {
				_ = recorderStore.Close()
			}
			printWorkerResult(job.RunID, worker.Result{
				SchemaVersion: worker.ResultSchemaVersion, RunID: job.RunID,
				Operation: job.Operation, ExecutionID: job.ExecutionID,
				Outcome: worker.OutcomeInfraFailed, Error: report.Error().Error(),
			})
			fatal("Worker preflight: %v", report.Error())
		}
	}
	engineOptions := []pipeline.Option{
		pipeline.WithRecorder(recorder),
		pipeline.WithApprovalStore(approvalStore),
		pipeline.WithLifecycleStore(lifecycleStore),
	}
	if businessBriefStore != nil {
		engineOptions = append(engineOptions, pipeline.WithBusinessBriefStore(businessBriefStore))
	}
	if candidateMetadataStore != nil {
		engineOptions = append(engineOptions, pipeline.WithCandidateMetadataStore(candidateMetadataStore))
	}
	if usageEnvelopeWriter != nil {
		engineOptions = append(engineOptions, pipeline.WithUsageEnvelopeWriter(usageEnvelopeWriter))
	}
	if terminalRecordWriter != nil {
		engineOptions = append(engineOptions, pipeline.WithTerminalRecordWriter(terminalRecordWriter))
	}
	if attemptManifestSource != nil || attemptManifestWriter != nil {
		engineOptions = append(engineOptions, pipeline.WithAttemptManifestStore(attemptManifestSource, attemptManifestWriter))
	}
	if eventLogSource != nil {
		engineOptions = append(engineOptions, pipeline.WithEventLogSource(eventLogSource))
	}
	if attestationWriter != nil {
		engineOptions = append(engineOptions, pipeline.WithAttestationWriter(attestationWriter))
	}
	if containmentReceiptWriter != nil {
		engineOptions = append(engineOptions, pipeline.WithContainmentReceiptWriter(containmentReceiptWriter))
	}
	if candidateEvidenceStore != nil {
		engineOptions = append(engineOptions, pipeline.WithCandidateEvidenceStore(candidateEvidenceStore))
	}
	engine := pipeline.NewRunEngine(pipeline.New(cfg, reg, engineOptions...))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if egressSocketSet {
		proxyURL, closeProxy, proxyErr := worker.StartOpenAIEgressBridge(ctx, egressSocket, egressToken)
		if proxyErr != nil {
			fatal("Worker OpenAI egress: %v", proxyErr)
		}
		defer closeProxy()
		if err := os.Setenv(runtime.OpenAIEgressProxyEnv, proxyURL); err != nil {
			fatal("Worker OpenAI egress environment: %v", err)
		}
	}
	var result pipeline.RunResult
	switch job.Operation {
	case worker.OperationStart:
		result, err = engine.Start(ctx, job.RunConfig())
	case worker.OperationResume:
		result, err = engine.Resume(ctx, pipeline.ResumeConfig{
			RunID: job.RunID, TargetDir: target, ApproveGates: job.ApproveGates,
			ApprovePlanHash: job.ApprovePlanHash,
		})
	case worker.OperationRecover:
		result, err = executeRecoveredJobWithSources(ctx, engine, target, job, eventLogSource, attemptManifestSource)
	case worker.OperationCancel:
		result, err = engine.Cancel(pipeline.CancelConfig{RunID: job.RunID, TargetDir: target})
	}
	if recorderStore != nil {
		_ = recorderStore.Close()
	}
	if err != nil {
		outcome := workerOutcomeFor(err)
		printWorkerResult(job.RunID, worker.Result{
			SchemaVersion: worker.ResultSchemaVersion, RunID: job.RunID,
			Operation: job.Operation, ExecutionID: job.ExecutionID,
			Outcome: outcome, Error: err.Error(),
		})
		fmt.Fprintf(os.Stderr, "worker %s остановлен: %v\n", job.RunID, err)
		os.Exit(exitCodeFor(err))
	}
	printWorkerResult(job.RunID, worker.Result{
		SchemaVersion: worker.ResultSchemaVersion, RunID: result.RunID,
		Operation: job.Operation, ExecutionID: job.ExecutionID,
		Outcome: string(result.Outcome),
	})
	logging.Printf("worker %s завершён: %s\n", result.RunID, result.Outcome)
}

type recoveryEngine interface {
	Start(context.Context, pipeline.RunConfig) (pipeline.RunResult, error)
	Resume(context.Context, pipeline.ResumeConfig) (pipeline.RunResult, error)
	RecoverInitialLifecycle(runID, targetDir, feature, task string) error
	LoadLifecycle(targetDir, runID string) (lifecycle.State, error)
	SaveLifecycle(targetDir string, previous, next lifecycle.State) error
}

func executeRecoveredJob(ctx context.Context, engine recoveryEngine, target string, job worker.Job) (pipeline.RunResult, error) {
	return executeRecoveredJobWithSources(ctx, engine, target, job, nil, nil)
}

func executeRecoveredJobWithSources(ctx context.Context, engine recoveryEngine, target string, job worker.Job, eventSource evidence.EventLog, manifestSource evidence.AttemptManifestSource) (pipeline.RunResult, error) {
	state, err := engine.LoadLifecycle(target, job.RunID)
	if errors.Is(err, fs.ErrNotExist) {
		runDir := filepath.Join(target, ".ai-team", "runs", job.RunID)
		if _, statErr := os.Stat(runDir); errors.Is(statErr, fs.ErrNotExist) {
			return engine.Start(ctx, job.RunConfig())
		} else if statErr != nil {
			return pipeline.RunResult{}, statErr
		}
		if err := engine.RecoverInitialLifecycle(job.RunID, target, job.Feature, job.Task); err != nil {
			return pipeline.RunResult{}, err
		}
		state, err = engine.LoadLifecycle(target, job.RunID)
	}
	if err != nil {
		return pipeline.RunResult{}, err
	}
	if outcome, attemptCount, terminalErr := recoveredTerminalOutcomeWithSources(target, job.RunID, state, eventSource, manifestSource); terminalErr == nil {
		if state.Phase != lifecycle.PhaseTerminal {
			terminal := state
			terminal.Phase = lifecycle.PhaseTerminal
			terminal.NextStage = ""
			terminal.PendingApprovalID = ""
			terminal.AttemptOrdinal = attemptCount
			if err := engine.SaveLifecycle(target, state, terminal); err != nil {
				return pipeline.RunResult{}, fmt.Errorf("reconcile terminal lifecycle: %w", err)
			}
		}
		// The worker child may be bubblewrap-isolated from controller receipt and
		// attestation stores. Its parent ProcessEngine runs the trusted
		// ReconcileTerminalDelivery callback after this command exits.
		result := pipeline.RunResult{RunID: job.RunID, Outcome: workflow.RunOutcome(outcome)}
		if outcome == worker.OutcomeFailed {
			return result, &pipeline.RunError{Outcome: workflow.RunFailed, Err: errors.New("recovered terminal run failed")}
		}
		return result, nil
	}
	return engine.Resume(ctx, pipeline.ResumeConfig{
		RunID: job.RunID, TargetDir: target, ApproveGates: job.ApproveGates,
		ApprovePlanHash: job.ApprovePlanHash,
	})
}

func recoveredTerminalOutcome(target, runID string, state lifecycle.State) (string, int, error) {
	return recoveredTerminalOutcomeWithSources(target, runID, state, nil, nil)
}

func recoveredTerminalOutcomeWithSources(target, runID string, state lifecycle.State, eventSource evidence.EventLog, manifestSource evidence.AttemptManifestSource) (string, int, error) {
	if state.RunID != runID || state.TargetDir != target {
		return "", 0, errors.New("terminal lifecycle identity mismatch")
	}
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	replayed, err := evidence.VerifyTerminalEvidenceWithSources(runDir, runID, target, manifestSource, eventSource)
	if err != nil {
		return "", 0, fmt.Errorf("terminal run evidence: %w", err)
	}
	switch replayed.Status {
	case workflow.RunCompleted, workflow.RunCompletedWithWarnings:
		return worker.OutcomeCompleted, len(replayed.Attempts), nil
	case workflow.RunFailed:
		return worker.OutcomeFailed, len(replayed.Attempts), nil
	case workflow.RunBlocked:
		return worker.OutcomeBlocked, len(replayed.Attempts), nil
	case workflow.RunStopped:
		return worker.OutcomeStopped, len(replayed.Attempts), nil
	case workflow.RunCanceled:
		return worker.OutcomeCanceled, len(replayed.Attempts), nil
	default:
		return "", 0, fmt.Errorf("unsupported durable terminal status %q", replayed.Status)
	}
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
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = flags.Parse(os.Args[2:])
	target, err := absoluteTarget(*targetValue)
	if err != nil {
		fatal("Scheduler worker target: %v", err)
	}
	requireControlRoot(target)
	workerAgentPaths := configuredAgentRegistryPaths()
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
	defer func() { _ = queue.Close() }() // закрытие на выходе из процесса: обработать ошибку уже негде.
	controllerRecorderStore, err := webstore.New(*webDB)
	if err != nil {
		fatal("Scheduler controller recorder: %v", err)
	}
	defer func() { _ = controllerRecorderStore.Close() }()
	controllerApprovalStore, err := approval.NewSQLiteStore(*webDB)
	if err != nil {
		fatal("Scheduler controller approvals: %v", err)
	}
	defer func() { _ = controllerApprovalStore.Close() }()
	workerOptions, err := configuredWorkerProcessOptions(
		worker.WithAgentRegistryPaths(workerAgentPaths),
		worker.WithControllerAPI(func() pipeline.Recorder { return web.NewStoreRecorder(controllerRecorderStore) }, controllerApprovalStore),
		worker.WithTerminalDeliveryReconciler(func(ctx context.Context, runID, targetDir string) error {
			registry, registryErr := newAgentRegistryWithPaths(targetDir, workerAgentPaths)
			if registryErr != nil {
				return fmt.Errorf("trusted delivery recovery registry: %w", registryErr)
			}
			cfg := loadValidatedConfig(targetDir, registry)
			engine := pipeline.NewRunEngine(pipeline.New(cfg, registry, pipeline.WithApprovalStore(controllerApprovalStore)))
			return engine.ReconcileTerminalDelivery(ctx, runID, targetDir)
		}),
	)
	if err != nil {
		fatal("Scheduler worker sandbox: %v", err)
	}
	processEngine, err := worker.NewProcessEngine([]string{*workerCommand}, target, *webDB, workerOptions...)
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
	dbPath := flags.String("db", "", "SQLite approvals DB внутри .ai-team (для trusted-local администратора)")
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
	var store interface {
		Decide(string, string, approval.Decision) (approval.PendingApproval, error)
	}
	var closeStore func() error
	if *dbPath == "" {
		fileStore, storeErr := approval.NewStore(absolute)
		if storeErr != nil {
			fatal("Ошибка approval store: %v", storeErr)
		}
		store = fileStore
	} else {
		controlRoot := filepath.Join(absolute, ".ai-team")
		if err := safeio.ValidateTree(controlRoot); err != nil {
			fatal("Небезопасный control root: %v", err)
		}
		resolvedDB := *dbPath
		if !filepath.IsAbs(resolvedDB) {
			resolvedDB = filepath.Join(absolute, resolvedDB)
		}
		resolvedDB, err = filepath.Abs(resolvedDB)
		if err != nil {
			fatal("Ошибка пути SQLite DB: %v", err)
		}
		relativeDB, err := filepath.Rel(controlRoot, resolvedDB)
		if err != nil || relativeDB == ".." || strings.HasPrefix(relativeDB, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeDB) {
			fatal("SQLite DB должна находиться внутри %s", controlRoot)
		}
		info, statErr := os.Lstat(resolvedDB)
		if statErr != nil || !info.Mode().IsRegular() {
			fatal("SQLite DB должна быть существующим обычным файлом внутри .ai-team")
		}
		sqliteStore, storeErr := approval.NewSQLiteStore(resolvedDB)
		if storeErr != nil {
			fatal("Ошибка SQLite approval store: %v", storeErr)
		}
		store = sqliteStore
		closeStore = sqliteStore.Close
	}
	if closeStore != nil {
		defer func() { _ = closeStore() }()
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
	return newAgentRegistryWithPaths(target, configuredAgentRegistryPaths())
}

func configuredAgentRegistryPaths() []string {
	paths := make([]string, 0)
	for _, pluginDir := range filepath.SplitList(os.Getenv("AI_TEAM_AGENT_PATH")) {
		if pluginDir == "" {
			continue
		}
		if absolute, err := filepath.Abs(pluginDir); err == nil {
			pluginDir = absolute
		}
		paths = append(paths, pluginDir)
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		userAgents := filepath.Join(configDir, "ai-team", "agents")
		// The built-in user registry is optional on a clean install. Keep it
		// when present, and preserve other filesystem errors so downstream path
		// validation still fails closed instead of silently hiding a problem.
		if _, statErr := os.Lstat(userAgents); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			paths = append(paths, userAgents)
		}
	}
	return paths
}

func newAgentRegistryWithPaths(target string, extraPaths []string) (*agent.Registry, error) {
	projectAgents := filepath.Join(target, ".ai-team", "agents")
	if err := safeio.ValidateTree(projectAgents); err != nil {
		return nil, err
	}
	layers := []agent.Layer{{Name: "project", FS: os.DirFS(filepath.Join(target, ".ai-team", "agents"))}}
	for index, pluginDir := range extraPaths {
		if pluginDir == "" {
			continue
		}
		layers = append(layers, agent.Layer{Name: fmt.Sprintf("plugin-%d:%s", index, pluginDir), FS: os.DirFS(pluginDir)})
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
	profileFlag := initFlags.String("profile", config.ProfileStandard, "Шаблон процесса: fast, standard или regulated")
	force := initFlags.Bool("force", false, "Перезаписать существующий .ai-team/config.yaml")
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
	data, err := cfg.Marshal()
	if err != nil {
		fatal("Ошибка сериализации конфига: %v", err)
	}
	if err := writeInitConfig(cfgPath, data, *force); err != nil {
		fatal("Ошибка создания конфига: %v", err)
	}

	if err := runtime.CheckCLI(cfg.CLI); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: %v\n", err)
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

// writeInitConfig preserves an existing config unless the caller explicitly
// requests replacement. Replacements use a temporary regular file and rename
// so a failed write never truncates the user's current config.
func writeInitConfig(path string, data []byte, force bool) error {
	existing, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return safeio.WriteRegularFileNoFollow(path, data, 0644)
	}
	if err != nil {
		return err
	}
	if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
		return fmt.Errorf("%s должен быть regular file без symlink", path)
	}
	if !force {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.yaml-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename replaces a regular file atomically and replaces (never follows) a
	// leaf symlink if another process raced this check. Refuse special files.
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
		return fmt.Errorf("%s изменился и больше не является regular file", path)
	}
	return os.Rename(tmpPath, path)
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
		// аварийный путь: значимая ошибка уже возвращается, Close только освобождает дескриптор.
		_ = file.Close()
		return err
	}
	return file.Close()
}

func loadValidatedConfig(target string, reg *agent.Registry) *config.Config {
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if err := safeio.RejectSymlink(cfgPath); err != nil {
		fatal("Небезопасный config path: %v", err)
	}
	cfg, overridden := e2eInMemoryLegacyConfig(target)
	var err error
	if !overridden {
		cfg, err = config.Load(cfgPath)
	}
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

	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = runFlags.Parse(os.Args[2:])
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

	// Read-only preflight ДО старта run, как у web-контроллера и worker
	// (AUD-09): та же классификация отсутствующих runtime/gh/origin, чтобы
	// CLI не откладывал обнаружение delivery-предусловий на поздние этапы.
	if err := runPreflight(cfg, reg, *target); err != nil {
		logging.Fail(logging.Record{
			Level: "error", Command: "run", Type: "preflight",
			Message: err.Error(), Exit: exitBlocked,
		}, "%s Preflight: %v", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(exitBlocked)
	}

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
		code := exitCodeFor(err)
		// Единственное место, где печатается финальная остановка run: все
		// терминальные исходы (BLOCKED, негативный вердикт, отказ resume,
		// отмена) приходят сюда одной ошибкой и печатаются один раз.
		logging.Fail(logging.Record{
			Level: "error", Command: "run", Type: "run",
			Message: "Пайплайн остановлен: " + err.Error(), Exit: code,
		}, "%s Пайплайн остановлен: %v", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(code)
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

// runPreflight выполняет read-only preflight ДО старта run (AUD-09): тот же
// pkg/preflight, что использует web-контроллер (control.WithPreflight) и
// worker, чтобы CLI/web/worker одинаково классифицировали отсутствующие
// runtime/gh/origin до runtime. Выбранное различие (зафиксировано в docs/reference/cli.md,
// «Preflight перед запуском»): CLI показывает отчёт read-only и блокирует
// только невозможность запустить runtime вовсе (check "cli"); git/gh/origin
// — предусловия поздних стадий (delivery), которые и так fail-closed
// проверяются на самой стадии, поэтому CLI не отказывает в run из-за них.
// runPreflight печатает read-only отчёт готовности и блокирует запуск только
// тогда, когда runtime невозможно запустить вовсе (check "cli").
//
// Различие с web-контроллером и worker, которые отклоняют Start при любой
// непройденной обязательной проверке, зафиксировано намеренно: Git, origin и
// gh — предусловия стадии доставки, и они fail-closed проверяются на ней самой.
// Локальный прогон, который остановится раньше доставки (отклонённое ревью,
// упавшие тесты, BLOCKED), законен и не должен требовать настроенного remote,
// авторизованного gh или даже Git-репозитория: вне Git контроллер использует
// полный hash snapshot (docs/ARCHITECTURE.md).
func runPreflight(cfg *config.Config, reg *agent.Registry, target string) error {
	report := preflight.New(cfg, reg, target).Check(context.Background())
	printPreflightReport(report)
	for _, check := range report.Checks {
		if check.ID == "cli" && check.Required && check.Status == preflight.StatusFailed {
			return fmt.Errorf("preflight failed: cli: %s", check.Message)
		}
	}
	return nil
}

// printPreflightReport печатает read-only отчёт preflight (runtime и
// delivery-предусловия) БДО старта дорогого run. Идёт через logging.Printf,
// чтобы в JSON-режиме stdout оставался чистым, а в quiet — подавлялся.
func printPreflightReport(report preflight.Report) {
	for _, check := range report.Checks {
		var label string
		switch check.Status {
		case preflight.StatusPassed:
			label = ui.ColoredStatus(true)
		case preflight.StatusWarning:
			label = ui.Colorize("!", ui.ColorYellow)
		case preflight.StatusFailed:
			label = ui.ColoredStatus(false)
		}
		required := ""
		if check.Required {
			required = " (required)"
		}
		logging.Printf("  %s %s%s: %s\n", label, check.ID, required, check.Message)
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
	// web store — best-effort проекция дашборда (выше уже есть degrade-путь): ошибка закрытия на выходе ни на что не влияет.
	return web.NewStoreRecorder(s), func() { _ = s.Close() }
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

	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = evalFlags.Parse(os.Args[2:])
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

// cmdCIImport импортирует ограниченный объяснимый набор checks из real project
// CI (P1-8) без исполнения произвольного YAML и печатает effective suite с
// fingerprint'ом ДО запуска. Сам command checks не выполняет.
func cmdCIImport() {
	ciFlags := flag.NewFlagSet("ci-import", flag.ExitOnError)
	target := ciFlags.String("target", ".", "Путь к проекту")
	format := ciFlags.String("format", string(ciimport.DefaultImportFormat),
		"Adopter-формат CI (github-actions)")
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = ciFlags.Parse(os.Args[2:])

	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absTarget
	requireControlRoot(*target)

	imp, err := ciimport.Import(*target, ciimport.ImportFormat(*format))
	if err != nil {
		fatal("Ошибка импорта CI: %v", err)
	}

	data := map[string]any{
		"format":       imp.Format,
		"workflows":    imp.WorkflowCount,
		"checks":       len(imp.Definitions),
		"skipped":      len(imp.Skipped),
		"fingerprint":  imp.Fingerprint,
		"source_files": imp.SourceFiles,
	}
	logging.Emit(logging.Record{
		Level:   "ok",
		Command: "ci-import",
		Type:    "ci-import",
		Message: fmt.Sprintf("CI import: %d checks (fingerprint %s)", len(imp.Definitions), shortFingerprint(imp.Fingerprint)),
		Data:    data,
	})
	logging.Emit(logging.Record{
		Level:   "info",
		Command: "ci-import",
		Type:    "ci-import-suite",
		Message: "Effective suite (до запуска):",
	})
	for _, d := range imp.Definitions {
		logging.Emit(logging.Record{
			Level:   "info",
			Command: "ci-import",
			Type:    "ci-import-check",
			Message: fmt.Sprintf("  %-16s %-10s %s", d.Name, d.Class, strings.Join(d.Command, " ")),
		})
	}
	for _, s := range imp.Skipped {
		logging.Emit(logging.Record{
			Level:   "info",
			Command: "ci-import",
			Type:    "ci-import-skipped",
			Message: fmt.Sprintf("  skip %s job=%s step=%d: %s", s.Source, s.Job, s.Step, s.Reason),
		})
	}
}

func shortFingerprint(fp string) string {
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
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

// cmdUsage печатает usage-сводку из controller state (cloud) или legacy local evidence.
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
	for _, components := range [][]string{
		{".ai-team", "state", "usage"},
		{".ai-team", "runs"},
		{".ai-team", "runs", runID},
	} {
		if err := checkExistingDirectoryChainNoSymlink(absolute, components...); err != nil {
			fatal("Небезопасный usage path: %v", err)
		}
	}
	envelope, err := metrics.ReadUsageEnvelope(absolute, runID)
	if errors.Is(err, os.ErrNotExist) {
		controllerStorePresent, stateErr := metrics.UsageEnvelopeReservation(absolute, runID)
		if stateErr != nil {
			fatal("Не удалось проверить controller usage state: %v", stateErr)
		}
		if controllerStorePresent {
			fatal("Не удалось прочитать controller usage envelope для run %s: %v", runID, err)
		}
		// Local CLI compatibility: older/local runs keep usage beside run.json.
		envelope, err = readLocalUsageEnvelope(absolute, runID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fatal("Не удалось прочитать usage: %v", err)
			}
			fatal("Повреждённый usage.json: %v", err)
		}
	} else if err != nil {
		fatal("Не удалось прочитать usage: %v", err)
	}
	lines := make([]string, 0, 4)
	if !envelope.HasCompleteTokenUsage() {
		lines = append(lines, "Входные токены: нет данных", "Выходные токены: нет данных", "Всего токенов: нет данных")
	} else {
		lines = append(lines,
			fmt.Sprintf("Входные токены: %d", envelope.TokensInput),
			fmt.Sprintf("Выходные токены: %d", envelope.TokensOutput),
			fmt.Sprintf("Всего токенов: %d", envelope.TokensInput+envelope.TokensOutput),
		)
	}
	lines = append(lines, fmt.Sprintf("Доля подписки (приблизительно): %s", approximateSubscriptionShare(absolute, envelope)))
	if _, err := io.WriteString(os.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		fatal("Ошибка вывода usage: %v", err)
	}
}

func approximateSubscriptionShare(target string, run metrics.UsageEnvelope) string {
	cfg, err := config.Load(filepath.Join(target, ".ai-team", "config.yaml"))
	if err != nil || cfg.Usage == nil || cfg.Usage.Validate() != nil || cfg.Usage.MonthlySubscriptionAmount <= 0 {
		return "оценка недоступна"
	}
	recorded, complete := recordedUsageEnvelopes(target)
	if !complete {
		return "оценка недоступна"
	}
	share, ok := metrics.EstimateSubscriptionShare(cfg.Usage.MonthlySubscriptionAmount, run, recorded)
	if !ok {
		return "оценка недоступна"
	}
	currency := cfg.Usage.MonthlySubscriptionCurrency
	if currency == "" {
		currency = "USD"
	}
	return fmt.Sprintf("≈ %.2f %s", share, currency)
}

func recordedUsageEnvelopes(target string) ([]metrics.UsageEnvelope, bool) {
	byRunID := make(map[string]metrics.UsageEnvelope)
	var envelopes []metrics.UsageEnvelope

	// Controller summaries outlive immutable run evidence. Enumerate them from
	// their durable store first so pruning .ai-team/runs does not erase known
	// usage from the subscription denominator.
	usageDir := filepath.Join(target, ".ai-team", "state", "usage")
	if err := checkExistingDirectoryChainNoSymlink(target, ".ai-team", "state", "usage"); err != nil {
		return nil, false
	}
	usageEntries, err := os.ReadDir(usageDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	reservedIDs := make(map[string]bool)
	envelopeIDs := make(map[string]bool)
	for _, entry := range usageEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".usage-") && strings.HasSuffix(name, ".tmp") {
			// Atomic writes can leave a temporary file after a process crash.
			// The reservation/envelope pair checks below still detect any
			// incomplete controller record.
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, false
		}
		// A valid run ID may itself end in ".reserved". In that case its
		// envelope filename also has the reservation suffix, so the filename
		// alone cannot distinguish the two records. The controller reservation
		// schema has a target_dir field that usage envelopes never have.
		data, readErr := safeio.ReadRegularFile(filepath.Join(usageDir, name), 8<<20)
		if readErr != nil {
			return nil, false
		}
		reservationRecord, classifyErr := isUsageReservationRecord(data)
		if classifyErr != nil {
			return nil, false
		}
		if reservationRecord {
			if !strings.HasSuffix(name, ".reserved.json") {
				return nil, false
			}
			runID := strings.TrimSuffix(name, ".reserved.json")
			if runID == "" || reservedIDs[runID] {
				return nil, false
			}
			reservedIDs[runID] = true
		} else {
			runID := strings.TrimSuffix(name, ".json")
			if runID == "" || envelopeIDs[runID] {
				return nil, false
			}
			envelopeIDs[runID] = true
		}
	}
	for runID := range reservedIDs {
		if !envelopeIDs[runID] {
			return nil, false
		}
	}
	for runID := range envelopeIDs {
		if !reservedIDs[runID] {
			return nil, false
		}
	}
	for runID := range reservedIDs {
		envelope, readErr := metrics.ReadUsageEnvelope(target, runID)
		if readErr != nil {
			return nil, false
		}
		byRunID[runID] = envelope
		envelopes = append(envelopes, envelope)
	}

	runsDir := filepath.Join(target, ".ai-team", "runs")
	if err := checkExistingDirectoryChainNoSymlink(target, ".ai-team", "runs"); err != nil {
		return nil, false
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	for _, entry := range entries {
		// Runs are expected to be real directories. In particular, don't let a
		// symlinked run disappear from the monthly denominator just because
		// DirEntry.IsDir reports false for symlinks. Check this before the
		// controller-ID dedup below so duplicate evidence cannot hide it either.
		runID := entry.Name()
		if runID == "." || runID == ".." {
			return nil, false
		}
		if err := checkExistingDirectoryChainNoSymlink(target, ".ai-team", "runs", runID); err != nil {
			return nil, false
		}
		info, infoErr := os.Lstat(filepath.Join(runsDir, runID))
		if infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, false
		}
		if _, alreadyRecorded := byRunID[runID]; alreadyRecorded {
			continue
		}
		reserved, reserveErr := metrics.UsageEnvelopeReservation(target, runID)
		if reserveErr != nil {
			return nil, false
		}
		var envelope metrics.UsageEnvelope
		if reserved {
			envelope, err = metrics.ReadUsageEnvelope(target, runID)
		} else {
			envelope, err = readLocalUsageEnvelope(target, runID)
		}
		if errors.Is(err, os.ErrNotExist) {
			// Without an envelope we cannot know whether this run belongs in
			// the current monthly denominator, so never publish a partial-share
			// estimate from the remaining recorded runs.
			return nil, false
		}
		if err != nil {
			return nil, false
		}
		byRunID[runID] = envelope
		envelopes = append(envelopes, envelope)
	}
	return envelopes, true
}

func isUsageReservationRecord(data []byte) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return false, err
	}
	if fields == nil {
		return false, errors.New("usage record must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return false, errors.New("usage record has trailing data")
		}
		return false, err
	}
	_, hasTargetDir := fields["target_dir"]
	return hasTargetDir, nil
}

func readLocalUsageEnvelope(target, runID string) (metrics.UsageEnvelope, error) {
	if err := checkExistingDirectoryChainNoSymlink(target, ".ai-team", "runs", runID); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	path := filepath.Join(target, ".ai-team", "runs", runID, "usage.json")
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil {
		return metrics.UsageEnvelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope metrics.UsageEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return metrics.UsageEnvelope{}, errors.New("trailing data")
		}
		return metrics.UsageEnvelope{}, err
	}
	if err := metrics.ValidateUsageEnvelope(runID, envelope); err != nil {
		return metrics.UsageEnvelope{}, err
	}
	return envelope, nil
}

// checkExistingDirectoryChainNoSymlink rejects symlinks and non-directory
// components while allowing a missing suffix. It keeps usage readers from
// following off-tree state even when the subscription estimate is disabled.
func checkExistingDirectoryChainNoSymlink(root string, components ...string) error {
	current := root
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component || strings.ContainsAny(component, `/\\`) {
			return fmt.Errorf("unsafe directory component %q", component)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s must be a directory without symlink", current)
		}
	}
	return nil
}

func cmdList() {
	listFlags := flag.NewFlagSet("list", flag.ExitOnError)
	targetValue := listFlags.String("target", ".", "Путь к целевому проекту")
	if err := listFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов list: %v", err)
	}
	// Позиционные аргументы у list смысла не имеют: молча их проглатывать —
	// значит скрывать от пользователя опечатку в команде.
	if listFlags.NArg() != 0 {
		fatal("Использование: ai-team list [--target <dir>]")
	}
	target, err := absoluteTarget(*targetValue)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	// Сам каталог target обязан существовать, иначе опечатка в пути молча
	// выдала бы built-in реестр за реестр указанного проекта. Отсутствие
	// .ai-team внутри — законный случай (показываем только built-in слой),
	// несуществующий каталог — нет.
	if _, err := safeio.ExistingDir(target); err != nil {
		if os.IsNotExist(err) {
			fatal("Каталог target не существует: %s", target)
		}
		fatal("Недоступный target: %v", err)
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
				logging.Fail(logging.Record{Level: "error", Command: "verify", Type: "gate_bundle", Message: err.Error(), Exit: exitFailed},
					"✗ Gate bundle %s: %v", arg, ui.Colorize(err.Error(), ui.ColorRed))
				os.Exit(exitFailed)
			}
			logging.Printf("✓ Gate bundle %s: OK — records согласованы, bundle_sha256 %s%s\n", arg, digest, sigNote(keyVerify))
			logging.Emit(logging.Record{Level: "ok", Command: "verify", Type: "gate_bundle", Message: "Gate bundle OK",
				Data: map[string]any{"bundle_sha256": digest}, Exit: exitOK})
			return
		}
		if err := export.VerifyBundle(arg, keyVerify); err != nil {
			logging.Fail(logging.Record{Level: "error", Command: "verify", Type: "run_bundle", Message: err.Error(), Exit: exitFailed},
				"✗ Bundle %s: %v", arg, ui.Colorize(err.Error(), ui.ColorRed))
			os.Exit(exitFailed)
		}
		logging.Printf("✓ Bundle %s: OK — records, event chain, anchor, attempt artifacts, delivery/containment records и attestation v1 согласованы%s\n", arg, sigNote(keyVerify))
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
		logging.Fail(logging.Record{Level: "error", Command: "verify", Type: "run", Message: err.Error(), Exit: exitFailed},
			"✗ Run %s: %v", runID, ui.Colorize(err.Error(), ui.ColorRed))
		os.Exit(exitFailed)
	}
	logging.Printf("✓ Run %s: anchor OK — run manifest, event chain, attempt artifacts, supplemental evidence и attestation v1 согласованы\n", runID)
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

func configuredWorkerProcessOptions(base ...worker.ProcessOption) ([]worker.ProcessOption, error) {
	options := append([]worker.ProcessOption(nil), base...)
	switch os.Getenv(worker.WorkerSandboxEnvVar) {
	case "":
		return options, nil
	case "bubblewrap":
		return append(options, worker.WithLinuxBubblewrapIsolation()), nil
	default:
		return nil, fmt.Errorf("%s поддерживает только пустое значение или bubblewrap", worker.WorkerSandboxEnvVar)
	}
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
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = webFlags.Parse(os.Args[2:])
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

	agentPaths := configuredAgentRegistryPaths()
	reg, err := newAgentRegistryWithPaths(target, agentPaths)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}
	cfg := loadValidatedConfig(target, reg)
	recorderStore, err := webstore.New(*dbPath)
	if err != nil {
		fatal("Ошибка recorder store: %v", err)
	}
	defer func() { _ = recorderStore.Close() }() // закрытие на выходе из процесса: обработать ошибку уже негде.
	approvalStore, err := approval.NewSQLiteStore(*dbPath)
	if err != nil {
		fatal("Ошибка approval store: %v", err)
	}
	defer func() { _ = approvalStore.Close() }() // общий web/controller approval store.
	if err := approvalStore.ImportLegacy(filepath.Join(target, ".ai-team", "state", "approvals")); err != nil {
		fatal("Не удалось безопасно перенести file approvals в web DB: %v", err)
	}
	localEngine := pipeline.NewRunEngine(pipeline.New(cfg, reg,
		pipeline.WithRecorder(web.NewStoreRecorder(recorderStore)),
		pipeline.WithApprovalStore(approvalStore)))
	controllerOptions := []control.Option{control.WithApprovalStore(approvalStore)}
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
		defer func() { _ = schedulerQueue.Close() }() // закрытие на выходе из процесса: обработать ошибку уже негде.
		queueEngine, queueErr := scheduler.NewQueueEngine(schedulerQueue, target)
		if queueErr != nil {
			fatal("Ошибка queue engine: %v", queueErr)
		}
		runController, err = control.New(queueEngine, target, controllerOptions...)
	} else if *workerCommand != "" {
		workerOptions, optionErr := configuredWorkerProcessOptions(
			worker.WithAgentRegistryPaths(agentPaths),
			worker.WithControllerAPI(func() pipeline.Recorder { return web.NewStoreRecorder(recorderStore) }, approvalStore),
			worker.WithTerminalDeliveryReconciler(func(ctx context.Context, runID, targetDir string) error {
				engine := pipeline.NewRunEngine(pipeline.New(cfg, reg, pipeline.WithApprovalStore(approvalStore)))
				return engine.ReconcileTerminalDelivery(ctx, runID, targetDir)
			}),
		)
		if optionErr != nil {
			fatal("Worker sandbox: %v", optionErr)
		}
		processEngine, processErr := worker.NewProcessEngine([]string{*workerCommand}, target, *dbPath, workerOptions...)
		if processErr != nil {
			fatal("Ошибка worker launcher: %v", processErr)
		}
		controllerOptions = append(controllerOptions, control.WithPreflight(preflight.New(cfg, reg, target)))
		runController, err = control.New(processEngine, target, controllerOptions...)
	} else {
		controllerOptions = append(controllerOptions, control.WithPreflight(preflight.New(cfg, reg, target)))
		runController, err = control.New(localEngine, target, controllerOptions...)
	}
	if err != nil {
		fatal("Ошибка run controller: %v", err)
	}
	serverOptions := []web.ServerOption{web.WithRunController(runController), web.WithTargetDir(target)}
	cloudAuthEnabled := false
	var localWebToken, localWebTokenPath string
	if secret := os.Getenv(*authSecretEnv); secret != "" {
		tokenManager, managerErr := cloudidentity.NewTokenManager([]byte(secret))
		if managerErr != nil {
			fatal("Ошибка cloud authentication: %v", managerErr)
		}
		serverOptions = append(serverOptions, web.WithAuthenticator(tokenManager))
		cloudAuthEnabled = true
	} else {
		token, tokenPath, tokenErr := loadOrCreateLocalWebToken(target)
		if tokenErr != nil {
			fatal("Ошибка локального web token: %v", tokenErr)
		}
		localAuthenticator, authErr := web.NewLocalAuthenticator(token)
		if authErr != nil {
			fatal("Ошибка локальной web authentication: %v", authErr)
		}
		serverOptions = append(serverOptions, web.WithLocalAuthenticator(localAuthenticator))
		localWebToken, localWebTokenPath = token, tokenPath
	}
	srv, err := web.NewServer(*dbPath, *distDir, *artifacts, serverOptions...)
	if err != nil {
		fatal("Ошибка запуска web сервера: %v", err)
	}
	if localWebToken != "" {
		if printErr := printLocalWebToken(os.Stderr, localWebToken, localWebTokenPath); printErr != nil {
			fatal("Не удалось вывести локальный web token: %v", printErr)
		}
	}
	defer func() { _ = srv.Close() }() // закрытие на выходе из процесса: обработать ошибку уже негде.
	// Фоновые ошибки run не должны исчезать после 202: фиксируем их в
	// SQLite projection дашборда.
	runController.SetFailureSink(srv.RecordAdmissionFailure)
	runController.SetAdmissionSink(srv.RecordQueuedJob)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if schedulerQueue != nil {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				reconcileSchedulerQueueOnce(recorderStore, schedulerQueue, srv, target)
			}
		}()
	}
	go func() {
		<-ctx.Done()
		// graceful shutdown best-effort: при его неудаче отложенный srv.Close() закрывает сервер принудительно.
		_ = srv.Shutdown(context.Background())
	}()

	addr := net.JoinHostPort(*host, *port)
	if !cloudAuthEnabled && *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
		fatal("web UI без cloud authentication может bind только loopback host")
	}
	logging.Printf("Web UI available at http://%s\n", addr)
	if err := srv.ListenAndServe(addr); err != nil {
		fatal("Ошибка сервера: %v", err)
	}
}

// reconcileSchedulerQueueOnce links durable dashboard admissions to scheduler
// jobs and projects early worker state. Keeping one reconciliation pass in a
// function lets tests exercise the exact startup recovery path deterministically.
func reconcileSchedulerQueueOnce(recorderStore *webstore.Store, schedulerQueue *scheduler.Queue, srv *web.Server, target string) {
	queuedRuns, listErr := recorderStore.QueuedRunIDs()
	if listErr != nil {
		return
	}
	for _, run := range queuedRuns {
		job, exists, getErr := schedulerQueue.Get(run.QueueJobID)
		if getErr == nil && exists {
			if job.Status == scheduler.StatusPending {
				if schedulerQueue.Activate(job.ID) != nil {
					continue
				}
				job, exists, getErr = schedulerQueue.Get(job.ID)
			}
			if getErr == nil && exists {
				srv.RecordQueueStatus(job.ID, string(job.Status), job.Error)
			}
		}
	}
	admissions, admissionErr := recorderStore.PendingAdmissions()
	if admissionErr != nil {
		return
	}
	for _, admission := range admissions {
		var command struct {
			Feature string `json:"feature"`
			Task    string `json:"task"`
		}
		if json.Unmarshal([]byte(admission.ConfigSnapshot), &command) != nil || command.Feature == "" || strings.TrimSpace(command.Task) == "" {
			continue
		}
		jobID, ensureErr := schedulerQueue.EnsureStartJob(worker.Job{
			SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
			RunID: admission.RunID, TargetDir: target, Feature: command.Feature, Task: command.Task,
		})
		if ensureErr != nil {
			continue
		}
		if srv.RecordQueuedJob(admission.RunID, jobID) != nil {
			continue
		}
		if schedulerQueue.Activate(jobID) != nil {
			continue
		}
		job, exists, getErr := schedulerQueue.Get(jobID)
		if getErr == nil && exists {
			srv.RecordQueueStatus(job.ID, string(job.Status), job.Error)
		}
	}
}
