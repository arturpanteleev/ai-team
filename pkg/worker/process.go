package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const maxDiagnostics = 64 << 10

// WorkerEnvAllowVar controls which parent environment variables are copied to
// disposable worker processes. Values are a comma-separated list of variable
// names; the selected names are also exposed to the nested agent runtime via
// AI_TEAM_HARNESS_ENV_ALLOW.
const WorkerEnvAllowVar = "AI_TEAM_WORKER_ENV_ALLOW"
const WorkerAgentPathsEnvVar = "AI_TEAM_WORKER_AGENT_PATHS"
const WorkerSandboxEnvVar = "AI_TEAM_WORKER_SANDBOX"

var workerEnvironmentBaseline = []string{
	"PATH", "USER", "LOGNAME", "SHELL", "PWD",
	"LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "NO_COLOR",
}

var workerEnvironmentReserved = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true,
	"TEMP": true, "TMP": true,
	"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
	"HOMEDRIVE": true, "HOMEPATH": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true,
	"XDG_DATA_HOME": true, "XDG_STATE_HOME": true,
	"AI_TEAM_AGENT_PATH": true, WorkerAgentPathsEnvVar: true,
	"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	WorkerEnvAllowVar: true, "AI_TEAM_HARNESS_ENV_ALLOW": true,
	WorkerSandboxEnvVar: true,
	WorkerAPIAddressEnv: true, WorkerAPITokenEnv: true,
}

type ProcessOption func(*ProcessEngine) error

type ProcessEngine struct {
	argv               []string
	target             string
	dbPath             string
	agentPaths         []string
	bubblewrap         bool
	apiRecorderFactory func() pipeline.Recorder
	apiApprovals       workerApprovalPort
}

type ProcessError struct {
	ExitCode    int
	Diagnostics string
	Result      *Result
	Err         error
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("disposable worker: %v: %s", e.Err, e.Diagnostics)
}

func (e *ProcessError) Unwrap() error { return e.Err }

func NewProcessEngine(argv []string, target, dbPath string, options ...ProcessOption) (*ProcessEngine, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("worker command обязателен")
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	absoluteTarget, err = filepath.EvalSymlinks(absoluteTarget)
	if err != nil {
		return nil, err
	}
	absoluteDB, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	engine := &ProcessEngine{
		argv: append([]string(nil), argv...), target: filepath.Clean(absoluteTarget), dbPath: filepath.Clean(absoluteDB),
	}
	for _, option := range options {
		if err := option(engine); err != nil {
			return nil, err
		}
	}
	return engine, nil
}

// WithAgentRegistryPaths supplies the exact non-project agent definition
// directories used by controller-side preflight to the disposable worker.
// The worker never inherits the controller's HOME or XDG config tree.
func WithAgentRegistryPaths(paths []string) ProcessOption {
	return func(engine *ProcessEngine) error {
		engine.agentPaths = make([]string, 0, len(paths))
		for _, path := range paths {
			if path == "" {
				return errors.New("пустой путь agent registry")
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			engine.agentPaths = append(engine.agentPaths, filepath.Clean(absolute))
		}
		return nil
	}
}

// WithControllerAPI moves worker recorder and approval traffic back to the
// controller process. The worker receives a short-lived, run-scoped loopback
// capability and no controller database path.
func WithControllerAPI(recorderFactory func() pipeline.Recorder, approvals pipeline.ApprovalStore) ProcessOption {
	return func(engine *ProcessEngine) error {
		if recorderFactory == nil || approvals == nil {
			return errors.New("controller API requires recorder and approval stores")
		}
		engine.apiRecorderFactory, engine.apiApprovals = recorderFactory, approvals
		return nil
	}
}

// WithLinuxBubblewrapIsolation opts disposable workers into the Linux
// bubblewrap filesystem namespace. It fails closed if the runtime is missing
// or unsupported; it never silently falls back to an unsandboxed process.
func WithLinuxBubblewrapIsolation() ProcessOption {
	return func(engine *ProcessEngine) error {
		if err := checkBubblewrapAvailable(); err != nil {
			return err
		}
		engine.bubblewrap = true
		return nil
	}
}

// AgentRegistryPathsFromEnvironment returns the exact agent path snapshot
// attached to a disposable worker invocation.
func AgentRegistryPathsFromEnvironment() ([]string, bool, error) {
	raw, exists := os.LookupEnv(WorkerAgentPathsEnvVar)
	if !exists {
		return nil, false, nil
	}
	var paths []string
	if err := strictjson.Unmarshal([]byte(raw), 1<<20, &paths); err != nil {
		return nil, true, fmt.Errorf("worker agent registry paths: %w", err)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, true, fmt.Errorf("worker agent registry path must be absolute and clean: %q", path)
		}
	}
	return paths, true, nil
}

// TargetDir is the mounted persistent workspace this worker is allowed to
// execute against. Scheduler polling uses it to filter claims.
func (e *ProcessEngine) TargetDir() string { return e.target }

func (e *ProcessEngine) Start(ctx context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: config.RunID, TargetDir: config.TargetDir, Feature: config.Feature, Task: config.TaskDesc,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	return e.execute(ctx, job)
}

func (e *ProcessEngine) Resume(ctx context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationResume,
		RunID: config.RunID, TargetDir: config.TargetDir,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	return e.execute(ctx, job)
}

func (e *ProcessEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationCancel,
		RunID: config.RunID, TargetDir: config.TargetDir,
	}
	return e.execute(context.Background(), job)
}

func (e *ProcessEngine) Execute(ctx context.Context, job Job) (pipeline.RunResult, error) {
	return e.execute(ctx, job)
}

func (e *ProcessEngine) execute(ctx context.Context, job Job) (pipeline.RunResult, error) {
	if e.bubblewrap && e.apiRecorderFactory == nil {
		return pipeline.RunResult{}, errors.New("bubblewrap cloud worker requires the controller API")
	}
	if err := job.ValidateQueued(e.target); err != nil {
		return pipeline.RunResult{}, err
	}
	var executionNonce [ExecutionIDBytes]byte
	if _, err := rand.Read(executionNonce[:]); err != nil {
		return pipeline.RunResult{}, fmt.Errorf("worker execution identity: %w", err)
	}
	job.SchemaVersion = SchemaVersion
	job.ExecutionID = hex.EncodeToString(executionNonce[:])
	if err := job.Validate(e.target); err != nil {
		return pipeline.RunResult{}, err
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return pipeline.RunResult{}, err
	}
	args := append(append([]string(nil), e.argv[1:]...), "worker", "--target", e.target)
	var api *workerAPIServer
	if e.apiRecorderFactory != nil {
		recorder := e.apiRecorderFactory()
		api, err = startWorkerAPIServer(job, recorder, e.apiApprovals)
		if err != nil {
			return pipeline.RunResult{}, fmt.Errorf("worker controller API: %w", err)
		}
		api.lifecycle, err = lifecycle.NewStore(e.target)
		if err != nil {
			api.close()
			return pipeline.RunResult{}, fmt.Errorf("worker lifecycle store: %w", err)
		}
		defer api.close()
	} else {
		// Compatibility path for local scheduler CLI configurations that have
		// not yet attached a controller-owned store API.
		args = append(args, "--db", e.dbPath)
	}
	command := exec.CommandContext(ctx, e.argv[0], args...)
	configureWorkerProcess(command)
	environment, cleanupEnvironment, err := workerProcessEnvironment(os.Environ(), e.agentPaths)
	if err != nil {
		return pipeline.RunResult{}, fmt.Errorf("worker environment: %w", err)
	}
	defer cleanupEnvironment()
	command.Env = environment
	if api != nil {
		command.Env = append(command.Env, workerAPIAddressEnv+"=http://"+api.listener.Addr().String(), workerAPITokenEnv+"="+api.token)
	}
	if e.bubblewrap {
		command, err = bubblewrapWorkerCommand(ctx, command, e.target, e.dbPath, e.agentPaths, command.Env)
		if err != nil {
			return pipeline.RunResult{}, fmt.Errorf("worker bubblewrap isolation: %w", err)
		}
		configureWorkerProcess(command)
		command.Env = environment
		if api != nil {
			command.Env = append(command.Env, workerAPIAddressEnv+"=http://"+api.listener.Addr().String(), workerAPITokenEnv+"="+api.token)
		}
	}
	command.Stdin = bytes.NewReader(payload)
	output := &limitedOutput{limit: maxDiagnostics}
	command.Stdout = output
	command.Stderr = output
	err = command.Run()
	result := pipeline.RunResult{RunID: job.RunID}
	if apiFailure := workerAPIRecorderFailure(output.String()); apiFailure != "" {
		return result, &ProcessError{ExitCode: processExitCode(err), Diagnostics: output.String(), Err: fmt.Errorf("worker controller API recorder failed: %s", apiFailure)}
	}
	if err == nil {
		// Успешный exit без строки результата — чужой binary: не считаем
		// это контролируемым завершением job.
		parsed, parseErr := ParseResult(output.String())
		if parseErr != nil {
			return result, &ProcessError{ExitCode: 0, Diagnostics: output.String(), Err: parseErr}
		}
		if err := parsed.ValidateFor(job); err != nil {
			return result, &ProcessError{ExitCode: 0, Diagnostics: output.String(), Err: err}
		}
		result.Outcome = workflow.RunOutcome(parsed.Outcome)
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
	}
	processErr := &ProcessError{ExitCode: exitCode, Diagnostics: output.String(), Err: err}
	if parsed, parseErr := ParseResult(output.String()); parseErr == nil && parsed.ValidateFor(job) == nil {
		parsedResult := parsed
		processErr.Result = &parsedResult
		result.Outcome = workflow.RunOutcome(parsed.Outcome)
	}
	return result, processErr
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

// workerProcessEnvironment constructs a purpose-limited environment for a
// disposable worker. HOME and TMPDIR point to private empty directories for
// this invocation; the controller's user config and credentials are not
// inherited through their conventional paths.
func workerProcessEnvironment(parent []string, agentPaths []string) ([]string, func(), error) {
	return workerProcessEnvironmentForOS(parent, agentPaths, runtime.GOOS)
}

func workerProcessEnvironmentForOS(parent []string, agentPaths []string, goos string) ([]string, func(), error) {
	return workerProcessEnvironmentForOSWithFileOps(parent, agentPaths, goos, os.Chmod, os.Mkdir)
}

// workerProcessEnvironmentForOSWithFileOps isolates filesystem setup so
// failure cleanup can be verified without relying on host filesystem quirks.
func workerProcessEnvironmentForOSWithFileOps(parent []string, agentPaths []string, goos string, chmod func(string, os.FileMode) error, mkdir func(string, os.FileMode) error) ([]string, func(), error) {
	keyForOS := func(key string) string { return environmentKeyForOS(key, goos) }
	values := make(map[string]string, len(parent))
	for _, item := range parent {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			values[keyForOS(key)] = value
		}
	}
	reserved := make(map[string]bool, len(workerEnvironmentReserved))
	for name := range workerEnvironmentReserved {
		reserved[keyForOS(name)] = true
	}

	var allowed []string
	seen := make(map[string]bool)
	for _, item := range strings.Split(values[keyForOS(WorkerEnvAllowVar)], ",") {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if !validEnvironmentName(key) {
			return nil, func() {}, fmt.Errorf("недопустимое имя переменной в %s: %q", WorkerEnvAllowVar, key)
		}
		key = keyForOS(key)
		if reserved[key] {
			return nil, func() {}, fmt.Errorf("%s не может переопределить изолированную переменную %s", WorkerEnvAllowVar, key)
		}
		if !seen[key] {
			seen[key] = true
			allowed = append(allowed, key)
		}
	}
	sort.Strings(allowed)

	workerHome, err := os.MkdirTemp("", "ai-team-worker-home-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(workerHome) }
	if err := chmod(workerHome, 0700); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	workerTemp := filepath.Join(workerHome, "tmp")
	if err := mkdir(workerTemp, 0700); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	baseline := append([]string(nil), workerEnvironmentBaseline...)
	if goos == "windows" {
		baseline = append(baseline, "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT")
	}
	environment := make(map[string]string, len(baseline)+len(allowed)+10)
	for _, key := range baseline {
		if value, ok := values[key]; ok {
			environment[key] = value
		}
	}
	if environment[keyForOS("PATH")] == "" {
		cleanup()
		return nil, func() {}, errors.New("PATH отсутствует в окружении контроллера")
	}
	environment["HOME"] = workerHome
	environment["TMPDIR"] = workerTemp
	if goos == "windows" {
		environment["TEMP"] = workerTemp
		environment["TMP"] = workerTemp
		appData := filepath.Join(workerHome, "AppData", "Roaming")
		localAppData := filepath.Join(workerHome, "AppData", "Local")
		if err := os.MkdirAll(appData, 0700); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		if err := os.MkdirAll(localAppData, 0700); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		environment["USERPROFILE"] = workerHome
		environment["APPDATA"] = appData
		environment["LOCALAPPDATA"] = localAppData
		if drive, homePath := windowsHomeDriveAndPath(workerHome, filepath.VolumeName); drive != "" {
			environment["HOMEDRIVE"] = drive
			environment["HOMEPATH"] = homePath
		}
	}
	environment["XDG_CONFIG_HOME"] = filepath.Join(workerHome, ".config")
	environment["XDG_CACHE_HOME"] = filepath.Join(workerHome, ".cache")
	environment["XDG_DATA_HOME"] = filepath.Join(workerHome, ".local", "share")
	environment["XDG_STATE_HOME"] = filepath.Join(workerHome, ".local", "state")
	if goos == "windows" {
		if environment["SYSTEMROOT"] == "" {
			environment["SYSTEMROOT"] = values["WINDIR"]
		}
		if environment["WINDIR"] == "" {
			environment["WINDIR"] = values["SYSTEMROOT"]
		}
		if environment["SYSTEMROOT"] == "" {
			cleanup()
			return nil, func() {}, errors.New("SystemRoot отсутствует в окружении Windows контроллера")
		}
		for _, key := range []string{"COMSPEC", "PATHEXT"} {
			if value, ok := values[key]; ok {
				environment[key] = value
			}
		}
	}
	for _, key := range allowed {
		if value, ok := values[key]; ok {
			environment[key] = value
		}
	}
	// Rebuild the downstream runtime allow-list from the selected names. Never
	// inherit a potentially broader or stale controller-side selector value.
	if len(allowed) > 0 {
		environment["AI_TEAM_HARNESS_ENV_ALLOW"] = strings.Join(allowed, ",")
	}
	registryPathsJSON, err := json.Marshal(agentPaths)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	environment[WorkerAgentPathsEnvVar] = string(registryPathsJSON)

	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result, cleanup, nil
}

func windowsHomeDriveAndPath(home string, volumeName func(string) string) (string, string) {
	drive := volumeName(home)
	if drive == "" {
		return "", ""
	}
	return drive, strings.TrimPrefix(home, drive)
}

func environmentKeyForOS(value, goos string) string {
	if goos == "windows" {
		return strings.ToUpper(value)
	}
	return value
}

func validEnvironmentName(value string) bool {
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' ||
			(index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

type limitedOutput struct {
	mu         sync.Mutex
	value      []byte
	limit      int
	truncated  bool
	apiFailure string
}

func (w *limitedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if failure := workerAPIRecorderFailure(string(value)); failure != "" {
		w.apiFailure = failure
	}
	remaining := w.limit - len(w.value)
	if remaining > 0 {
		count := len(value)
		if count > remaining {
			count = remaining
		}
		w.value = append(w.value, value[:count]...)
	}
	if len(value) > remaining {
		w.truncated = true
	}
	return len(value), nil
}

func (w *limitedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	value := string(w.value)
	if w.truncated {
		value += "\n[diagnostics truncated]"
	}
	if w.apiFailure != "" && workerAPIRecorderFailure(value) == "" {
		value += "\n" + workerAPIErrorMarker + w.apiFailure
	}
	return value
}
