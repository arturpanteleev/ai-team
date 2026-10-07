package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// contract_test.go — issue #119: worker исполняет задания в многопользовательском
// сценарии, поэтому проверяются именно guard'ы протокола: какой job считается
// валидным, что попадает в pipeline.RunConfig и как классифицируется падение
// дочернего процесса (перезапускать job или нет).

// TestJobValidateRejectsMalformed — Validate решает, будет ли чужой JSON
// исполнен как задание. Каждый отвергаемый случай — отдельный способ выйти
// за mounted target или запустить не ту операцию.
func TestJobValidateRejectsMalformed(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	base := func() Job {
		return Job{
			SchemaVersion: SchemaVersion, Operation: OperationStart,
			RunID: "run-1", TargetDir: target, Feature: "feature", Task: "задача", ExecutionID: strings.Repeat("a", ExecutionIDBytes*2),
		}
	}
	if err := base().Validate(target); err != nil {
		t.Fatalf("эталонный job должен быть валиден: %v", err)
	}
	recovery := base()
	recovery.Operation = OperationRecover
	if err := recovery.Validate(target); err != nil {
		t.Fatalf("recovery должен сохранять admission identity: %v", err)
	}

	cases := []struct {
		name     string
		mutate   func(*Job)
		expected string
	}{
		{"чужая schema_version", func(j *Job) { j.SchemaVersion = SchemaVersion + 1 }, "schema_version"},
		{"пустая execution identity", func(j *Job) { j.ExecutionID = "" }, "execution_id"},
		{"короткая execution identity", func(j *Job) { j.ExecutionID = "deadbeef" }, "execution_id"},
		{"пустой run_id", func(j *Job) { j.RunID = "" }, "run_id"},
		{"run_id с разделителем пути", func(j *Job) { j.RunID = "../escape" }, "run_id"},
		{"run_id с обратным слэшем", func(j *Job) { j.RunID = `a\b` }, "run_id"},
		{"относительный target_dir", func(j *Job) { j.TargetDir = "relative/dir" }, "absolute"},
		{"несуществующий target_dir", func(j *Job) { j.TargetDir = filepath.Join(target, "absent") }, "недоступен"},
		{"start без feature", func(j *Job) { j.Feature = "" }, "feature и task"},
		{"start с недопустимой feature", func(j *Job) { j.Feature = "../escape" }, "feature и task"},
		{"start с пустым task", func(j *Job) { j.Task = "   " }, "feature и task"},
		{"resume с feature", func(j *Job) { j.Operation = OperationResume; j.Task = "" }, "resume"},
		{"recover без исходной задачи", func(j *Job) { j.Operation = OperationRecover; j.Task = "" }, "recover"},
		{"cancel с task", func(j *Job) { j.Operation = OperationCancel; j.Feature = "" }, "cancel"},
		{"cancel с approve_plan_hash", func(j *Job) {
			j.Operation = OperationCancel
			j.Feature, j.Task = "", ""
			j.ApprovePlanHash = strings.Repeat("a", 64)
		}, "cancel"},
		{"неизвестная операция", func(j *Job) { j.Operation = Operation("delete") }, "operation"},
	}
	for _, testCase := range cases {
		job := base()
		testCase.mutate(&job)
		err := job.Validate(target)
		if err == nil {
			t.Fatalf("%s: job должен быть отклонён", testCase.name)
		}
		if !strings.Contains(err.Error(), testCase.expected) {
			t.Fatalf("%s: диагностика должна называть %q, получено: %v", testCase.name, testCase.expected, err)
		}
	}

	// Пустой expectedTarget снимает только сверку с mounted target: сам job
	// по-прежнему обязан пройти остальные проверки.
	foreign := base()
	foreign.TargetDir = filepath.Clean(t.TempDir())
	if err := foreign.Validate(target); err == nil {
		t.Fatal("job чужого target обязан отклоняться")
	}
	if err := foreign.Validate(""); err != nil {
		t.Fatalf("без mounted target job должен приниматься: %v", err)
	}
}

// TestJobRunConfigCarriesIdentityAndApproval — RunConfig переносит job в
// контракт pipeline. Потеря ApprovePlanHash здесь означала бы, что
// одобренный человеком план не доезжает до enforcement'а.
func TestJobRunConfigCarriesIdentityAndApproval(t *testing.T) {
	planHash := strings.Repeat("b", 64)
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: "run-7", TargetDir: "/srv/repo", Feature: "billing", Task: "починить счёт",
		ApproveGates: true, ApprovePlanHash: planHash,
	}
	config := job.RunConfig()
	if config.RunID != job.RunID || config.Feature != job.Feature ||
		config.TaskDesc != job.Task || config.TargetDir != job.TargetDir {
		t.Fatalf("identity job потеряна в RunConfig: %+v", config)
	}
	if !config.ApproveGates || config.ApprovePlanHash != planHash {
		t.Fatalf("approval-параметры не доехали до RunConfig: %+v", config)
	}
}

// TestParseResultRejectsMalformed — строка результата решает, считается ли
// job завершённым контролируемо. Чужой binary, старая схема и мусор обязаны
// отличаться от честного результата.
func TestParseResultRejectsMalformed(t *testing.T) {
	valid := fmt.Sprintf(`%s{"schema_version":%d,"run_id":"run-1","operation":"start","execution_id":"%s","outcome":"completed"}`,
		ResultPrefix, ResultSchemaVersion, strings.Repeat("a", ExecutionIDBytes*2))

	parsed, err := ParseResult("шум\n" + valid + "\nхвост\n")
	if err != nil {
		t.Fatalf("валидный результат среди постороннего вывода: %v", err)
	}
	if parsed.Outcome != OutcomeCompleted || parsed.RunID != "run-1" {
		t.Fatalf("неожиданный результат: %+v", parsed)
	}
	legacy := ResultPrefix + `{"schema_version":1,"run_id":"run-1","operation":"start","outcome":"completed"}` + "\n"
	if _, err := ParseResult(legacy); err == nil || !strings.Contains(err.Error(), "неподдерживаемая schema_version 1") {
		t.Fatalf("v1 result must be rejected with an explicit version error, got %v", err)
	}

	if _, err := ParseResult(valid + "\n" + valid + "\n"); err == nil {
		t.Fatal("повторный result line обязан отклоняться")
	}

	cases := map[string]string{
		"без строки результата": "просто вывод\n",
		"не JSON":                ResultPrefix + "not-json\n",
		"неизвестное поле":       ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","extra":1}` + "\n",
		"чужая схема":            ResultPrefix + `{"schema_version":99,"outcome":"completed"}` + "\n",
		"пустой outcome":         ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":""}` + "\n",
		"нет execution identity": ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","outcome":"completed"}` + "\n",
		"admin operation":        ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","admin_operation":"grant_role"}` + "\n",
		"human decision":         ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","approval_decision":{"action":"approve"}}` + "\n",
		"artifact path":          ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","artifacts":[{"path":"../../approvals.json"}]}` + "\n",
		"oversized error":        ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","error":"` + strings.Repeat("x", MaxResultErrorBytes+1) + `"}` + "\n",
		"oversized":              ResultPrefix + `{"schema_version":3,"run_id":"run-1","operation":"start","execution_id":"` + strings.Repeat("a", ExecutionIDBytes*2) + `","outcome":"completed","error":"` + strings.Repeat("x", MaxResultBytes) + `"}` + "\n",
		"trailing JSON":          valid + ` {"run_id":"other"}` + "\n",
	}
	for name, output := range cases {
		if _, err := ParseResult(output); err == nil {
			t.Fatalf("%s: результат обязан отклоняться", name)
		}
	}
}

// TestResultControlledSeparatesDurableFromRetryable — controlled-исход
// означает «job дошёл до durable состояния, перезапускать нельзя».
func TestResultControlledSeparatesDurableFromRetryable(t *testing.T) {
	for _, outcome := range []string{
		OutcomeCompleted, OutcomeWaitingApproval, OutcomeBlocked, OutcomeStopped, OutcomeCanceled,
	} {
		if !(Result{Outcome: outcome}).Controlled() {
			t.Fatalf("durable исход %q обязан быть controlled", outcome)
		}
	}
	for _, outcome := range []string{OutcomeFailed, OutcomeInfraFailed, "", "unknown"} {
		if (Result{Outcome: outcome}).Controlled() {
			t.Fatalf("исход %q не должен считаться controlled (job перезапускаем)", outcome)
		}
	}
}

func TestResultMustMatchTheLaunchedJob(t *testing.T) {
	job := Job{RunID: "run-1", Operation: OperationResume, ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
	valid := Result{RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, Outcome: OutcomeWaitingApproval}
	if err := valid.ValidateFor(job); err != nil {
		t.Fatalf("matching result rejected: %v", err)
	}
	for name, result := range map[string]Result{
		"other run":       {RunID: "run-2", Operation: job.Operation, Outcome: OutcomeCompleted},
		"other action":    {RunID: job.RunID, Operation: OperationCancel, ExecutionID: job.ExecutionID, Outcome: OutcomeCompleted},
		"other execution": {RunID: job.RunID, Operation: job.Operation, ExecutionID: strings.Repeat("b", ExecutionIDBytes*2), Outcome: OutcomeCompleted},
	} {
		if err := result.ValidateFor(job); err == nil {
			t.Errorf("%s result should be rejected", name)
		}
	}
}

func TestProcessEngineRejectsWrongJobAndOperationResults(t *testing.T) {
	target := t.TempDir()
	for _, mode := range []string{"wrong-run", "wrong-operation", "wrong-execution"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AI_TEAM_WORKER_TEST_MODE", mode)
			allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_TEST_MODE")
			engine, err := NewProcessEngine(
				[]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"},
				target, filepath.Join(target, "web.db"),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.Execute(context.Background(), Job{
				SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "expected-run",
				TargetDir: target, Feature: "feature", Task: "task",
			})
			var processErr *ProcessError
			if !errors.As(err, &processErr) || processErr.Result != nil {
				t.Fatalf("untrusted mismatched result must fail closed, got %v", err)
			}
		})
	}
}

func TestProcessEngineRejectsReplayedExecutionResult(t *testing.T) {
	target := t.TempDir()
	replayPath := filepath.Join(t.TempDir(), "execution-id")
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "replay-execution")
	t.Setenv("AI_TEAM_WORKER_REPLAY_ID", replayPath)
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_TEST_MODE", "AI_TEAM_WORKER_REPLAY_ID")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"},
		target, filepath.Join(target, "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: "same-run", TargetDir: target, Feature: "feature", Task: "task"}
	if _, err := engine.Execute(context.Background(), job); err != nil {
		t.Fatalf("первый invocation должен принять собственный результат: %v", err)
	}
	_, err = engine.Execute(context.Background(), job)
	var processErr *ProcessError
	if !errors.As(err, &processErr) || processErr.Result != nil || !strings.Contains(err.Error(), "execution_id") {
		t.Fatalf("повтор старого результата должен быть отклонён: %v", err)
	}
}

func TestProcessEngineAssignsFreshExecutionIdentityAndUpgradesQueuedV1(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "worker-job.json")
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "echo")
	t.Setenv("AI_TEAM_WORKER_TEST_MARKER", marker)
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_TEST_MODE", "AI_TEAM_WORKER_TEST_MARKER")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"},
		target, filepath.Join(target, "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: LegacyQueueSchemaVersion, Operation: OperationStart,
		RunID: "run-old-queue", TargetDir: target, Feature: "feature", Task: "task"}
	var previous string
	for i := 0; i < 2; i++ {
		if _, err := engine.Execute(context.Background(), job); err != nil {
			t.Fatalf("legacy durable job invocation %d: %v", i+1, err)
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		var invoked Job
		if err := json.Unmarshal(data, &invoked); err != nil {
			t.Fatal(err)
		}
		if invoked.SchemaVersion != SchemaVersion || !validExecutionID(invoked.ExecutionID) {
			t.Fatalf("process invocation not upgraded/bound: %+v", invoked)
		}
		if previous != "" && previous == invoked.ExecutionID {
			t.Fatal("повторный invocation должен получить новый execution_id")
		}
		previous = invoked.ExecutionID
	}
}

// TestNewProcessEngineRejectsUnusableConfig — движок не должен создаваться
// без исполняемого worker command и с недоступным target.
func TestNewProcessEngineRejectsUnusableConfig(t *testing.T) {
	target := t.TempDir()
	db := filepath.Join(target, "web.db")
	if _, err := NewProcessEngine(nil, target, db); err == nil {
		t.Fatal("пустой argv обязан отклоняться")
	}
	if _, err := NewProcessEngine([]string{""}, target, db); err == nil {
		t.Fatal("пустая команда обязана отклоняться")
	}
	if _, err := NewProcessEngine([]string{"ai-team"}, filepath.Join(target, "absent"), db); err == nil {
		t.Fatal("несуществующий target обязан отклоняться")
	}
}

func TestBubblewrapWorkerRequiresControllerAPI(t *testing.T) {
	target := t.TempDir()
	engine, err := NewProcessEngine([]string{"unused-worker"}, target, filepath.Join(target, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine.bubblewrap = true
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "sandbox-api-required", Feature: "feature", TaskDesc: "task", TargetDir: target,
	}); err == nil || !strings.Contains(err.Error(), "requires the controller API") {
		t.Fatalf("sandbox without controller API must fail before spawning: %v", err)
	}
}

func TestProcessEngineRejectsControllerLifecycleStoreBeforeSpawn(t *testing.T) {
	target := t.TempDir()
	stateDir := filepath.Join(target, ".ai-team", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runs"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := NewProcessEngine(
		[]string{"worker-must-not-run"}, target, filepath.Join(target, "controller.db"),
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "lifecycle-store-failure", Feature: "feature", TaskDesc: "task", TargetDir: target,
	})
	if err == nil || !strings.Contains(err.Error(), "worker lifecycle store") {
		t.Fatalf("invalid controller lifecycle store must fail before spawning worker, got %v", err)
	}
}

// newTestEngine — ProcessEngine, запускающий helper-тест этого пакета в роли
// `ai-team worker`.
func newTestEngine(t *testing.T, target string) *ProcessEngine {
	t.Helper()
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_TEST_MODE", "AI_TEAM_WORKER_TEST_MARKER", "AI_TEAM_WORKER_REPLAY_ID")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func allowWorkerTestEnvironment(t *testing.T, names ...string) {
	t.Helper()
	t.Setenv(WorkerEnvAllowVar, strings.Join(names, ","))
}

func TestProcessEngineDoesNotInheritControllerSecrets(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "worker-environment.json")
	controllerHome := t.TempDir()
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "env")
	t.Setenv("HOME", controllerHome)
	t.Setenv("OPENAI_API_KEY", "provider-test-key")
	t.Setenv("AI_TEAM_AUTH_SECRET", "auth-test-secret")
	t.Setenv("AI_TEAM_SIGNING_KEY", "signing-test-secret")
	t.Setenv("AI_TEAM_DB_PASSWORD", "database-test-secret")
	t.Setenv("AI_TEAM_WORKER_TEST_ENV_MARKER", marker)
	allowWorkerTestEnvironment(t, "OPENAI_API_KEY", "AI_TEAM_WORKER_TEST_ENV_MARKER", "AI_TEAM_WORKER_TEST_MODE")

	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"},
		target, filepath.Join(target, "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "run-env", Feature: "feature", TaskDesc: "task", TargetDir: target,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.Unmarshal(data, &environment); err != nil {
		t.Fatal(err)
	}
	if environment["OPENAI_API_KEY"] != "provider-test-key" {
		t.Fatalf("explicit provider key missing from worker: %v", environment)
	}
	for _, key := range []string{"AI_TEAM_AUTH_SECRET", "AI_TEAM_SIGNING_KEY", "AI_TEAM_DB_PASSWORD"} {
		if _, exists := environment[key]; exists {
			t.Fatalf("controller secret %s leaked to worker", key)
		}
	}
	if environment["AI_TEAM_HARNESS_ENV_ALLOW"] != "AI_TEAM_WORKER_TEST_ENV_MARKER,AI_TEAM_WORKER_TEST_MODE,OPENAI_API_KEY" {
		t.Fatalf("nested harness allow-list not reconstructed: %q", environment["AI_TEAM_HARNESS_ENV_ALLOW"])
	}
	if environment["HOME"] == controllerHome || environment["HOME"] == "" {
		t.Fatalf("worker inherited controller HOME: %q", environment["HOME"])
	}
	if _, err := os.Stat(environment["HOME"]); !os.IsNotExist(err) {
		t.Fatalf("per-invocation worker HOME should be removed after exit, stat err=%v", err)
	}
}

func TestWorkerEnvironmentWindowsNamesAndSystemBaseline(t *testing.T) {
	parent := []string{
		"Path=C:\\Windows\\System32;C:\\Tools",
		"SystemRoot=C:\\Windows",
		"TEMP=C:\\ControllerTemp",
		"TMP=C:\\ControllerTemp",
		"AI_TEAM_WORKER_ENV_ALLOW=OPENAI_API_KEY",
		"openai_api_key=selected-key",
		"HOME=C:\\ControllerHome",
	}
	result, cleanup, err := workerProcessEnvironmentForOS(parent, []string{`C:\agents`}, "windows")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	values := make(map[string]string, len(result))
	for _, entry := range result {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", entry)
		}
		if _, exists := values[key]; exists {
			t.Fatalf("duplicate environment key after normalization: %s", key)
		}
		values[key] = value
	}
	if values["PATH"] != `C:\Windows\System32;C:\Tools` || values["SYSTEMROOT"] != `C:\Windows` {
		t.Fatalf("Windows system baseline was not preserved: %v", values)
	}
	if values["OPENAI_API_KEY"] != "selected-key" {
		t.Fatalf("case-insensitive selected variable missing: %v", values)
	}
	for _, key := range []string{"TEMP", "TMP", "TMPDIR"} {
		if values[key] == "" || strings.Contains(values[key], "ControllerTemp") {
			t.Fatalf("worker temp variable %s was not isolated: %q", key, values[key])
		}
	}
	if values["USERPROFILE"] != values["HOME"] || values["APPDATA"] == "" || values["LOCALAPPDATA"] == "" {
		t.Fatalf("Windows profile paths must stay in private worker home: %v", values)
	}
	if values["AI_TEAM_WORKER_AGENT_PATHS"] != `["C:\\agents"]` {
		t.Fatalf("worker agent registry snapshot missing: %q", values["AI_TEAM_WORKER_AGENT_PATHS"])
	}
	for _, reserved := range []string{"home", "pAtH", "Temp", "SystemRoot", "UserProfile", "aPpDaTa", "AI_TEAM_AGENT_PATH"} {
		bad := append(append([]string(nil), parent...), "AI_TEAM_WORKER_ENV_ALLOW="+reserved)
		if _, _, err := workerProcessEnvironmentForOS(bad, nil, "windows"); err == nil {
			t.Errorf("case-variant reserved name %q was accepted", reserved)
		}
	}
}

func TestAgentRegistryPathsOptionAndEnvironmentSnapshot(t *testing.T) {
	target := t.TempDir()
	paths := []string{filepath.Join("agents", "..", "shared-agents"), filepath.Join(target, "agents")}
	engine, err := NewProcessEngine([]string{"worker"}, target, filepath.Join(target, "web.db"), WithAgentRegistryPaths(paths))
	if err != nil {
		t.Fatal(err)
	}
	if len(engine.agentPaths) != len(paths) {
		t.Fatalf("registry paths count = %d, want %d", len(engine.agentPaths), len(paths))
	}
	for index, path := range paths {
		absolute, absErr := filepath.Abs(path)
		if absErr != nil || engine.agentPaths[index] != filepath.Clean(absolute) {
			t.Fatalf("registry path %d = %q, want absolute clean %q (err=%v)", index, engine.agentPaths[index], filepath.Clean(absolute), absErr)
		}
	}

	if _, err := NewProcessEngine([]string{"worker"}, target, filepath.Join(target, "web.db"), WithAgentRegistryPaths([]string{"agents", ""})); err == nil {
		t.Fatal("empty registry path must be rejected")
	}

	previous, wasSet := os.LookupEnv(WorkerAgentPathsEnvVar)
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(WorkerAgentPathsEnvVar, previous)
		} else {
			_ = os.Unsetenv(WorkerAgentPathsEnvVar)
		}
	})
	_ = os.Unsetenv(WorkerAgentPathsEnvVar)
	if got, exists, err := AgentRegistryPathsFromEnvironment(); err != nil || exists || got != nil {
		t.Fatalf("absent snapshot = (%v, %v, %v), want (nil, false, nil)", got, exists, err)
	}

	encoded, err := json.Marshal(engine.agentPaths)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(WorkerAgentPathsEnvVar, string(encoded))
	got, exists, err := AgentRegistryPathsFromEnvironment()
	if err != nil || !exists || len(got) != len(engine.agentPaths) {
		t.Fatalf("valid snapshot = (%v, %v, %v)", got, exists, err)
	}
	for index := range got {
		if got[index] != engine.agentPaths[index] {
			t.Fatalf("snapshot[%d] = %q, want %q", index, got[index], engine.agentPaths[index])
		}
	}
}

func TestAgentRegistryPathsEnvironmentRejectsMalformedSnapshots(t *testing.T) {
	for _, raw := range []string{
		"{", `{}`, `"/tmp/agents"`, `["relative/agents"]`, `["/tmp/agents/../other"]`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(WorkerAgentPathsEnvVar, raw)
			if _, exists, err := AgentRegistryPathsFromEnvironment(); !exists || err == nil {
				t.Fatalf("malformed snapshot %q must be present and rejected; exists=%v err=%v", raw, exists, err)
			}
		})
	}
}

func TestWorkerProcessEnvironmentIsolationAndCleanup(t *testing.T) {
	parent := []string{
		"PATH=/controller/bin", "USER=worker-test", "HOME=/controller/home",
		"XDG_CONFIG_HOME=/controller/config", "AI_TEAM_AGENT_PATH=/controller/agents",
		"AI_TEAM_HARNESS_ENV_ALLOW=stale,controller", "AI_TEAM_WORKER_ENV_ALLOW= ZED, MISSING_KEY, TEST_TOKEN,TEST_TOKEN ",
		"ZED=zed-value", "TEST_TOKEN=token-value", "BROKEN_ENTRY", "=empty-name",
	}
	result, cleanup, err := workerProcessEnvironmentForOS(parent, []string{"/shared/agents"}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(result))
	for _, entry := range result {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("environment entry missing separator: %q", entry)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %q", key)
		}
		values[key] = value
	}
	if values["PATH"] != "/controller/bin" || values["USER"] != "worker-test" || values["TEST_TOKEN"] != "token-value" || values["ZED"] != "zed-value" {
		t.Fatalf("baseline and explicitly allowed values missing: %v", values)
	}
	if values["AI_TEAM_HARNESS_ENV_ALLOW"] != "MISSING_KEY,TEST_TOKEN,ZED" {
		t.Fatalf("nested allow-list should be normalized, deduplicated, and sorted: %q", values["AI_TEAM_HARNESS_ENV_ALLOW"])
	}
	if _, exists := values["MISSING_KEY"]; exists {
		t.Fatal("selected variable absent from controller should not be synthesized")
	}
	if values["XDG_CONFIG_HOME"] == "/controller/config" || values["XDG_CONFIG_HOME"] != filepath.Join(values["HOME"], ".config") {
		t.Errorf("XDG_CONFIG_HOME should be redirected into worker home: %q", values["XDG_CONFIG_HOME"])
	}
	if _, exists := values["AI_TEAM_AGENT_PATH"]; exists {
		t.Error("controller AI_TEAM_AGENT_PATH leaked into worker environment")
	}
	if values["HOME"] == "/controller/home" || values["HOME"] == "" || values["TMPDIR"] == "" {
		t.Fatalf("worker home/temp were not isolated: home=%q tmp=%q", values["HOME"], values["TMPDIR"])
	}
	home := values["HOME"]
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		t.Fatalf("worker home should exist as a directory before cleanup: info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatalf("worker home permissions = %o, want 0700 on POSIX", info.Mode().Perm())
	}
	if values[WorkerAgentPathsEnvVar] != `["/shared/agents"]` {
		t.Fatalf("registry snapshot serialization = %q", values[WorkerAgentPathsEnvVar])
	}
	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("worker home should be removed by cleanup; stat err=%v", err)
	}
}

func TestWindowsHomeDriveAndPathNormalization(t *testing.T) {
	drive, homePath := windowsHomeDriveAndPath(`C:\Users\worker`, func(string) string { return `C:` })
	if drive != `C:` || homePath != `\Users\worker` {
		t.Fatalf("Windows home volume normalization = (%q, %q)", drive, homePath)
	}
	if drive, homePath := windowsHomeDriveAndPath("/private/home", func(string) string { return "" }); drive != "" || homePath != "" {
		t.Fatalf("path without a volume = (%q, %q), want empty pair", drive, homePath)
	}
}

func TestWorkerProcessEnvironmentRejectsInvalidAndReservedNames(t *testing.T) {
	for _, name := range []string{"A-B", "1TOKEN", "TOKEN=VALUE", "\u2603"} {
		t.Run("invalid/"+name, func(t *testing.T) {
			parent := []string{"PATH=/bin", WorkerEnvAllowVar + "=" + name}
			if _, cleanup, err := workerProcessEnvironmentForOS(parent, nil, "linux"); err == nil {
				cleanup()
				t.Fatalf("invalid environment name %q was accepted", name)
			}
		})
	}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "AI_TEAM_AGENT_PATH", WorkerAgentPathsEnvVar, WorkerEnvAllowVar, WorkerAPIAddressEnv, WorkerAPITokenEnv} {
		t.Run("reserved/"+name, func(t *testing.T) {
			parent := []string{"PATH=/bin", WorkerEnvAllowVar + "=" + name}
			if _, cleanup, err := workerProcessEnvironmentForOS(parent, nil, "linux"); err == nil {
				cleanup()
				t.Fatalf("reserved environment name %q was accepted", name)
			}
		})
	}

	valid := []string{"A", "TOKEN_2", "_UNDERSCORE", "a9"}
	for _, name := range valid {
		if !validEnvironmentName(name) {
			t.Errorf("valid environment name %q was rejected", name)
		}
	}
	for _, name := range []string{"", "9TOKEN"} {
		if validEnvironmentName(name) {
			t.Errorf("invalid environment name %q was accepted", name)
		}
	}
}

func TestWorkerProcessEnvironmentRequiresPathAndWindowsRoot(t *testing.T) {
	if _, cleanup, err := workerProcessEnvironmentForOS([]string{"USER=test"}, nil, "linux"); err == nil {
		cleanup()
		t.Fatal("missing PATH must fail")
	}
	if _, cleanup, err := workerProcessEnvironmentForOS([]string{"PATH=/bin"}, nil, "windows"); err == nil {
		cleanup()
		t.Fatal("missing Windows SystemRoot/WINDIR must fail")
	}
	withoutAllowList, cleanup, err := workerProcessEnvironmentForOS([]string{"PATH=/bin", "AI_TEAM_HARNESS_ENV_ALLOW=stale"}, nil, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range withoutAllowList {
		if strings.HasPrefix(entry, "AI_TEAM_HARNESS_ENV_ALLOW=") {
			cleanup()
			t.Fatalf("stale downstream allow-list should be dropped when no variables are selected: %q", entry)
		}
	}
	cleanup()

	for _, rootKey := range []string{"SYSTEMROOT", "WINDIR"} {
		parent := []string{"PATH=C:\\Windows\\System32", rootKey + "=C:\\Windows", "COMSPEC=C:\\Windows\\cmd.exe", "PATHEXT=.COM;.EXE"}
		result, cleanup, err := workerProcessEnvironmentForOS(parent, nil, "windows")
		if err != nil {
			t.Fatalf("Windows root provided by %s: %v", rootKey, err)
		}
		values := make(map[string]string)
		for _, entry := range result {
			key, value, _ := strings.Cut(entry, "=")
			values[key] = value
		}
		if values["SYSTEMROOT"] != `C:\Windows` || values["WINDIR"] != `C:\Windows` ||
			values["COMSPEC"] != `C:\Windows\cmd.exe` || values["PATHEXT"] != `.COM;.EXE` {
			cleanup()
			t.Fatalf("Windows baseline normalization failed for %s: %v", rootKey, values)
		}
		home := values["HOME"]
		cleanup()
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("Windows environment cleanup failed for %s: %v", rootKey, err)
		}
	}
}

func TestWorkerProcessEnvironmentReportsTempCreationFailure(t *testing.T) {
	base := t.TempDir()
	tempFile := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(tempFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempFile)
	// os.TempDir uses TMPDIR on Unix and TMP/TEMP/USERPROFILE on Windows.
	// Point every platform-specific candidate at the file so MkdirTemp must fail.
	t.Setenv("TMP", tempFile)
	t.Setenv("TEMP", tempFile)
	t.Setenv("USERPROFILE", tempFile)
	if _, cleanup, err := workerProcessEnvironmentForOS([]string{"PATH=/bin"}, nil, "linux"); err == nil {
		cleanup()
		t.Fatal("worker home creation under a file must fail")
	}
}

func TestWorkerProcessEnvironmentCleansHomeAfterPermissionFailure(t *testing.T) {
	permissionErr := errors.New("chmod denied")
	var createdHome string
	_, _, err := workerProcessEnvironmentForOSWithFileOps([]string{"PATH=/bin"}, nil, "linux", func(path string, mode os.FileMode) error {
		createdHome = path
		if mode != 0700 {
			t.Errorf("worker home mode = %o, want 0700", mode)
		}
		return permissionErr
	}, os.Mkdir)
	if !errors.Is(err, permissionErr) {
		t.Fatalf("permission error should be returned unchanged: %v", err)
	}
	if createdHome == "" {
		t.Fatal("permission callback was not invoked")
	}
	if _, err := os.Stat(createdHome); !os.IsNotExist(err) {
		t.Fatalf("partially initialized worker home should be removed, stat err=%v", err)
	}
}

func TestWorkerProcessEnvironmentCleansHomeAfterTempDirectoryFailure(t *testing.T) {
	mkdirErr := errors.New("worker temp directory unavailable")
	var workerTemp string
	_, _, err := workerProcessEnvironmentForOSWithFileOps([]string{"PATH=/bin"}, nil, "linux", os.Chmod, func(path string, mode os.FileMode) error {
		workerTemp = path
		if mode != 0700 {
			t.Errorf("worker temp mode = %o, want 0700", mode)
		}
		if filepath.Base(path) != "tmp" {
			t.Errorf("mkdir path = %q, want private tmp directory", path)
		}
		return mkdirErr
	})
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("temp directory error should be returned unchanged: %v", err)
	}
	if workerTemp == "" {
		t.Fatal("mkdir callback was not invoked")
	}
	if _, err := os.Stat(filepath.Dir(workerTemp)); !os.IsNotExist(err) {
		t.Fatalf("partially initialized worker home should be removed, stat err=%v", err)
	}
}

// TestProcessEngineResumeAndCancelBuildTypedJobs — Resume/Cancel обязаны
// отправлять именно свою операцию и не протаскивать execution-параметры,
// которые Validate запрещает для cancel.
func TestProcessEngineResumeAndCancelBuildTypedJobs(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "job.json")
	t.Setenv("AI_TEAM_WORKER_TEST_MARKER", marker)
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "echo")
	engine := newTestEngine(t, target)

	planHash := strings.Repeat("c", 64)
	if _, err := engine.Resume(context.Background(), pipeline.ResumeConfig{
		RunID: "run-resume", TargetDir: target, ApproveGates: true, ApprovePlanHash: planHash,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	job := readMarkerJob(t, marker)
	if job.Operation != OperationResume || job.RunID != "run-resume" {
		t.Fatalf("неожиданный resume job: %+v", job)
	}
	if job.Feature != "" || job.Task != "" {
		t.Fatalf("resume не должен нести feature/task: %+v", job)
	}
	if !job.ApproveGates || job.ApprovePlanHash != planHash {
		t.Fatalf("resume обязан переносить approval-параметры: %+v", job)
	}

	if _, err := engine.Cancel(pipeline.CancelConfig{RunID: "run-cancel", TargetDir: target}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	job = readMarkerJob(t, marker)
	if job.Operation != OperationCancel || job.RunID != "run-cancel" {
		t.Fatalf("неожиданный cancel job: %+v", job)
	}
	if job.ApproveGates || job.ApprovePlanHash != "" || job.Feature != "" || job.Task != "" {
		t.Fatalf("cancel не должен нести execution-параметры: %+v", job)
	}
}

// TestProcessEngineRejectsInvalidJobBeforeSpawn — невалидный job не должен
// доходить до запуска процесса.
func TestProcessEngineRejectsInvalidJobBeforeSpawn(t *testing.T) {
	target := t.TempDir()
	engine := newTestEngine(t, target)
	_, err := engine.Execute(context.Background(), Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: "run-1", TargetDir: filepath.Clean(t.TempDir()), Feature: "f", Task: "t",
	})
	if err == nil || !strings.Contains(err.Error(), "mounted target") {
		t.Fatalf("job чужого target обязан отклоняться до spawn, получено: %v", err)
	}
}

func TestProcessEngineRejectsInvalidWorkerEnvironmentBeforeSpawn(t *testing.T) {
	target := t.TempDir()
	engine := newTestEngine(t, target)
	allowWorkerTestEnvironment(t, "INVALID-NAME")
	_, err := engine.Execute(context.Background(), Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: "run-env-invalid", TargetDir: engine.TargetDir(), Feature: "feature", Task: "task",
	})
	if err == nil || !strings.Contains(err.Error(), "worker environment") {
		t.Fatalf("invalid worker environment must prevent child spawn: %v", err)
	}
}

// TestProcessEngineClassifiesChildFailures — падение дочернего процесса:
// с честной строкой результата исход бизнесовый (job не перезапускается),
// без неё — инфраструктурный сбой с диагностикой.
func TestProcessEngineClassifiesChildFailures(t *testing.T) {
	t.Run("падение со строкой результата", func(t *testing.T) {
		target := t.TempDir()
		t.Setenv("AI_TEAM_WORKER_TEST_MODE", "fail-with-result")
		result, err := newTestEngine(t, target).Start(context.Background(), pipeline.RunConfig{
			RunID: "run-blocked", Feature: "feature", TaskDesc: "задача", TargetDir: target,
		})
		var processErr *ProcessError
		if !errors.As(err, &processErr) {
			t.Fatalf("ожидалась *ProcessError, получено: %v", err)
		}
		if processErr.ExitCode != 2 {
			t.Fatalf("exit code дочернего процесса потерян: %d", processErr.ExitCode)
		}
		if processErr.Result == nil || processErr.Result.Outcome != OutcomeBlocked {
			t.Fatalf("строка результата обязана дочитываться при ненулевом exit: %+v", processErr.Result)
		}
		if result.Outcome != workflow.RunOutcome(OutcomeBlocked) {
			t.Fatalf("исход обязан доезжать до RunResult: %+v", result)
		}
		if !strings.Contains(processErr.Error(), "disposable worker") {
			t.Fatalf("диагностика должна называть источник: %q", processErr.Error())
		}
		if processErr.Unwrap() == nil {
			t.Fatal("ProcessError обязан разворачиваться в причину")
		}
	})

	t.Run("падение без строки результата", func(t *testing.T) {
		target := t.TempDir()
		t.Setenv("AI_TEAM_WORKER_TEST_MODE", "fail-no-result")
		_, err := newTestEngine(t, target).Start(context.Background(), pipeline.RunConfig{
			RunID: "run-crash", Feature: "feature", TaskDesc: "задача", TargetDir: target,
		})
		var processErr *ProcessError
		if !errors.As(err, &processErr) {
			t.Fatalf("ожидалась *ProcessError, получено: %v", err)
		}
		if processErr.Result != nil {
			t.Fatalf("без строки результата исход не должен выдумываться: %+v", processErr.Result)
		}
		if !strings.Contains(processErr.Diagnostics, "паника воркера") {
			t.Fatalf("диагностика дочернего процесса потеряна: %q", processErr.Diagnostics)
		}
	})

	t.Run("нулевой exit без строки результата", func(t *testing.T) {
		target := t.TempDir()
		t.Setenv("AI_TEAM_WORKER_TEST_MODE", "no-result")
		_, err := newTestEngine(t, target).Start(context.Background(), pipeline.RunConfig{
			RunID: "run-silent", Feature: "feature", TaskDesc: "задача", TargetDir: target,
		})
		var processErr *ProcessError
		if !errors.As(err, &processErr) {
			t.Fatalf("успешный exit без результата — чужой binary, ожидалась *ProcessError: %v", err)
		}
		if !strings.Contains(processErr.Err.Error(), "result line") {
			t.Fatalf("ожидалась диагностика об отсутствии строки результата: %v", processErr.Err)
		}
	})
}

// TestLimitedOutputTruncatesDiagnostics — диагностика дочернего процесса
// попадает в память контроллера, поэтому она обязана быть ограничена и
// честно помечена как усечённая.
func TestLimitedOutputTruncatesDiagnostics(t *testing.T) {
	output := &limitedOutput{limit: 8}
	if count, err := output.Write([]byte("12345")); count != 5 || err != nil {
		t.Fatalf("Write вернул %d, %v", count, err)
	}
	// Writer обязан отчитываться о полном объёме, иначе io.Copy решит, что
	// запись оборвалась (ErrShortWrite), и убьёт чтение вывода.
	if count, err := output.Write([]byte("6789abc")); count != 7 || err != nil {
		t.Fatalf("Write вернул %d, %v", count, err)
	}
	value := output.String()
	if !strings.HasPrefix(value, "12345678") {
		t.Fatalf("сохранён неверный префикс: %q", value)
	}
	if strings.Contains(value, "abc") {
		t.Fatalf("данные сверх лимита обязаны отбрасываться: %q", value)
	}
	if !strings.Contains(value, "truncated") {
		t.Fatalf("усечение обязано быть помечено: %q", value)
	}
	apiOutput := &limitedOutput{limit: 8}
	_, _ = apiOutput.Write([]byte(strings.Repeat("x", 20)))
	_, _ = apiOutput.Write([]byte(workerAPIErrorMarker + "recorder rejected"))
	if value := apiOutput.String(); !strings.Contains(value, workerAPIErrorMarker+"recorder rejected") {
		t.Fatalf("API failure marker lost after diagnostics truncation: %q", value)
	}
}

func readMarkerJob(t *testing.T, marker string) Job {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("worker process не записал job: %v", err)
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatalf("маркер не парсится: %v (%s)", err, data)
	}
	return job
}

// TestWorkerProtocolHelper — точка входа дочернего процесса в роли
// `ai-team worker`. Поведение выбирается AI_TEAM_WORKER_TEST_MODE.
func TestWorkerProtocolHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), " worker ") {
		return
	}
	switch os.Getenv("AI_TEAM_WORKER_TEST_MODE") {
	case "wait":
		marker := os.Getenv("AI_TEAM_WORKER_TEST_MARKER")
		if marker == "" {
			t.Fatal("wait helper requires a start marker")
		}
		if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "no-result":
		os.Exit(0)
	case "fail-no-result":
		fmt.Fprintln(os.Stderr, "паника воркера: nil map")
		os.Exit(7)
	case "fail-with-result":
		job := decodeHelperJob(t)
		printHelperResult(t, job.ExecutionID, "run-blocked", OperationStart, OutcomeBlocked)
		os.Exit(2)
	case "echo", "env", "wrong-run", "wrong-operation", "wrong-execution", "replay-execution", "api-failure":
		job := decodeHelperJob(t)
		if os.Getenv("AI_TEAM_WORKER_TEST_MODE") == "api-failure" {
			forged := job
			forged.RunID = "another-run"
			port, err := NewWorkerAPIPort(forged)
			if err != nil {
				t.Fatal(err)
			}
			NewWorkerAPIRecorder(port).RunStarted(forged.RunID, "feature", "snapshot", time.Now())
		}
		data, _ := json.Marshal(job)
		if marker := os.Getenv("AI_TEAM_WORKER_TEST_MARKER"); marker != "" {
			if err := os.WriteFile(marker, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		resultRunID, resultOperation := job.RunID, job.Operation
		if os.Getenv("AI_TEAM_WORKER_TEST_MODE") == "wrong-run" {
			resultRunID = "forged-run"
		}
		if os.Getenv("AI_TEAM_WORKER_TEST_MODE") == "wrong-operation" {
			resultOperation = OperationCancel
		}
		resultExecutionID := job.ExecutionID
		switch os.Getenv("AI_TEAM_WORKER_TEST_MODE") {
		case "wrong-execution":
			resultExecutionID = strings.Repeat("0", ExecutionIDBytes*2)
		case "replay-execution":
			replayPath := os.Getenv("AI_TEAM_WORKER_REPLAY_ID")
			if previous, readErr := os.ReadFile(replayPath); readErr == nil {
				resultExecutionID = string(previous)
			} else if !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			} else if writeErr := os.WriteFile(replayPath, []byte(job.ExecutionID), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		printHelperResult(t, resultExecutionID, resultRunID, resultOperation, OutcomeCompleted)
		os.Exit(0)
	}
	os.Exit(0)
}

func decodeHelperJob(t *testing.T) Job {
	t.Helper()
	target := ""
	for index := range os.Args {
		if os.Args[index] == "--target" && index+1 < len(os.Args) {
			target = os.Args[index+1]
		}
	}
	job, err := DecodeJob(os.Stdin, target)
	if err != nil {
		t.Fatal(err)
	}
	if marker := os.Getenv("AI_TEAM_WORKER_TEST_ENV_MARKER"); marker != "" {
		environment := make(map[string]string)
		for _, key := range []string{
			"OPENAI_API_KEY", "AI_TEAM_AUTH_SECRET", "AI_TEAM_SIGNING_KEY", "AI_TEAM_DB_PASSWORD",
			"AI_TEAM_HARNESS_ENV_ALLOW", "AI_TEAM_WORKER_ENV_ALLOW", "HOME",
		} {
			if value, exists := os.LookupEnv(key); exists {
				environment[key] = value
			}
		}
		data, _ := json.Marshal(environment)
		if err := writeMarkerAtomically(marker, data); err != nil {
			t.Fatal(err)
		}
	}
	if marker := os.Getenv("AI_TEAM_WORKER_ARGS_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte(strings.Join(os.Args, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func printHelperResult(t *testing.T, executionID, runID string, operation Operation, outcome string) {
	t.Helper()
	data, err := json.Marshal(Result{
		SchemaVersion: ResultSchemaVersion, RunID: runID, Operation: operation, ExecutionID: executionID, Outcome: outcome,
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", ResultPrefix, data)
}
