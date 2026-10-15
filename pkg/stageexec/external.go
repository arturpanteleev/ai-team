package stageexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/dockerrun"
	"github.com/arturpanteleev/ai-team/pkg/process"
	"github.com/arturpanteleev/ai-team/pkg/redact"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"gopkg.in/yaml.v3"
)

// ProtocolVersion is the version of the JSON request/response contract.
const ProtocolVersion = 1

const (
	defaultTimeout        = 10 * time.Minute
	maxTimeout            = 24 * time.Hour
	defaultMaxOutputBytes = 1 << 20
	defaultMaxResultBytes = 1 << 20
	hardMaxBytes          = 64 << 20
	maxMetadataEntries    = 32
	maxMetadataValueBytes = 512
	maxSummaryBytes       = 2048
	// minSecretBytes is the shortest allowed-env value masked in the log; it
	// matches the gate runner threshold so both redactors behave alike.
	minSecretBytes    = 4
	defaultPath       = "/usr/local/bin:/usr/bin:/bin"
	reservedEnvPrefix = "AI_TEAM_"
)

var (
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	metadataKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	adapterNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// AdapterSpec is the trusted configuration of one named external executor.
// The argv comes from the config, never from a model or from stage output, and
// is executed without a shell.
type AdapterSpec struct {
	// Argv is the command and its arguments. A first element containing a path
	// separator is resolved against the working directory.
	Argv []string `yaml:"argv"`
	// Timeout bounds one run (Go duration); default 10m.
	Timeout string `yaml:"timeout,omitempty"`
	// MaxOutputBytes caps the captured stderr log; default 1 MiB.
	MaxOutputBytes int64 `yaml:"max_output_bytes,omitempty"`
	// MaxResultBytes caps the JSON result on stdout; default 1 MiB.
	MaxResultBytes int64 `yaml:"max_result_bytes,omitempty"`
	// EnvAllow lists the only controller environment variables handed to the
	// program. Everything else is dropped.
	EnvAllow []string `yaml:"env_allow,omitempty"`
	// Docker runs the program in a container instead of on the host: argv is
	// then the command inside the image. The JSON contract is unchanged.
	Docker *DockerSpec `yaml:"docker,omitempty"`
}

// UnmarshalYAML rejects unknown keys: a typo in a limit name would otherwise
// silently run the program without that limit.
func (s *AdapterSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		allowed := map[string]bool{"argv": true, "timeout": true, "max_output_bytes": true, "max_result_bytes": true, "env_allow": true, "docker": true}
		for index := 0; index+1 < len(node.Content); index += 2 {
			if key := node.Content[index].Value; !allowed[key] {
				return fmt.Errorf("config: executor adapter: неизвестный ключ %q", key)
			}
		}
	}
	type plain AdapterSpec
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = AdapterSpec(decoded)
	return nil
}

// ValidateAdapterName reports whether name is a valid adapter identifier.
func ValidateAdapterName(name string) bool { return adapterNamePattern.MatchString(name) }

// Validate checks the spec; name is the adapter key for messages.
func (s AdapterSpec) Validate(name string) error {
	var errs []string
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf("executor_adapters.%s: %s", name, fmt.Sprintf(format, args...)))
	}
	if !adapterNamePattern.MatchString(name) {
		add("невалидное имя адаптера")
	}
	if len(s.Argv) == 0 {
		add("argv обязателен")
	}
	for index, arg := range s.Argv {
		if strings.ContainsRune(arg, 0) {
			add("argv[%d] содержит NUL", index)
		}
	}
	if len(s.Argv) > 0 && strings.TrimSpace(s.Argv[0]) == "" {
		add("argv[0] не может быть пустым")
	}
	if s.Timeout != "" {
		if d, err := time.ParseDuration(s.Timeout); err != nil || d <= 0 || d > maxTimeout {
			add("timeout %q не парсится или вне (0, %s] (пример: 5m)", s.Timeout, maxTimeout)
		}
	}
	if s.MaxOutputBytes < 0 || s.MaxOutputBytes > hardMaxBytes {
		add("max_output_bytes вне диапазона [0, %d]", hardMaxBytes)
	}
	if s.MaxResultBytes < 0 || s.MaxResultBytes > hardMaxBytes {
		add("max_result_bytes вне диапазона [0, %d]", hardMaxBytes)
	}
	seen := map[string]bool{}
	for _, env := range s.EnvAllow {
		if !envNamePattern.MatchString(env) {
			add("env_allow: невалидное имя %q", env)
			continue
		}
		if strings.HasPrefix(env, reservedEnvPrefix) {
			add("env_allow: префикс %s зарезервирован контроллером", reservedEnvPrefix)
		}
		if seen[env] {
			add("env_allow: %q повторяется", env)
		}
		seen[env] = true
	}
	if s.Docker != nil {
		if err := s.Docker.Validate(); err != nil {
			add("docker: %v", err)
		}
		for _, env := range s.EnvAllow {
			if dockerrun.ClientEnvName(env) {
				add("env_allow: %q зарезервирована клиентом docker и не передаётся в контейнер", env)
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (s AdapterSpec) timeout() time.Duration {
	if d, err := time.ParseDuration(s.Timeout); err == nil && d > 0 {
		return d
	}
	return defaultTimeout
}

func (s AdapterSpec) maxOutput() int64 {
	if s.MaxOutputBytes > 0 {
		return s.MaxOutputBytes
	}
	return defaultMaxOutputBytes
}

func (s AdapterSpec) maxResult() int64 {
	if s.MaxResultBytes > 0 {
		return s.MaxResultBytes
	}
	return defaultMaxResultBytes
}

// Launcher starts the adapter process. The host launcher runs argv directly;
// a Docker launcher can replace it while keeping the JSON contract unchanged.
type Launcher interface {
	// Run executes the invocation, feeding stdin and filling stdout/stderr,
	// and returns when the process exits or ctx ends (killing the process).
	Run(ctx context.Context, launch Launch) error
}

// Launch is the fully resolved process invocation.
type Launch struct {
	Argv   []string
	Dir    string
	Env    []string
	Stdin  []byte
	Stdout *LimitedBuffer
	Stderr *LimitedBuffer
	// Request is the stage request the launch serves. A container launcher
	// rebuilds the JSON request from it with container paths, so no host path
	// reaches the program.
	Request Request
	// Provenance is filled by the launcher with what the controller observed
	// about the execution environment (image, network, mounts). It is merged
	// into the attempt metadata and wins over program-supplied keys.
	Provenance map[string]string
	// StagedFiles is filled by a container launcher with result files the
	// program wrote (output name to bytes). Execute writes them to the declared
	// paths only after the response passed the contract check.
	StagedFiles map[string][]byte
}

// HostLauncher runs the program on the host under process-group supervision.
type HostLauncher struct{}

// Run implements Launcher.
func (HostLauncher) Run(ctx context.Context, launch Launch) error {
	program := launch.Argv[0]
	if strings.ContainsRune(program, filepath.Separator) && !filepath.IsAbs(program) {
		program = filepath.Join(launch.Dir, program)
	}
	command := exec.Command(program, launch.Argv[1:]...) //nolint:gosec // argv is trusted config, executed without a shell.
	command.Dir = launch.Dir
	command.Env = launch.Env
	command.Stdin = bytes.NewReader(launch.Stdin)
	command.Stdout = launch.Stdout
	command.Stderr = launch.Stderr
	return process.Run(ctx, command)
}

// External runs a named adapter for one stage attempt.
type External struct {
	name     string
	spec     AdapterSpec
	launcher Launcher
	environ  func() []string
}

// NewExternal creates the host executor for an adapter spec.
func NewExternal(name string, spec AdapterSpec) *External {
	var launcher Launcher = HostLauncher{}
	if spec.Docker != nil {
		launcher = DockerLauncher{Settings: *spec.Docker}
	}
	return &External{name: name, spec: spec, launcher: launcher, environ: os.Environ}
}

// WithLauncher replaces the process launcher (container executors, tests).
func (e *External) WithLauncher(launcher Launcher) *External {
	e.launcher = launcher
	return e
}

// Identity reports the adapter name.
func (e *External) Identity() Identity {
	return Identity{Kind: KindExternal, Name: e.name}
}

type requestInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type requestDocument struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	AttemptID     string         `json:"attempt_id"`
	Stage         string         `json:"stage"`
	Feature       string         `json:"feature"`
	Task          string         `json:"task"`
	Instructions  string         `json:"instructions"`
	WorkDir       string         `json:"workdir"`
	ArtifactRoot  string         `json:"artifact_root"`
	Inputs        []requestInput `json:"inputs"`
	Outputs       []Output       `json:"outputs"`
	Mutation      string         `json:"mutation,omitempty"`
	AllowedPaths  []string       `json:"allowed_paths,omitempty"`
}

type resultOutput struct {
	Content *string `json:"content,omitempty"`
}

type resultDocument struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        string                  `json:"status"`
	Error         string                  `json:"error,omitempty"`
	Summary       string                  `json:"summary,omitempty"`
	Verdict       string                  `json:"verdict,omitempty"`
	Outputs       map[string]resultOutput `json:"outputs,omitempty"`
	Metadata      map[string]string       `json:"metadata,omitempty"`
}

// Execute runs the program once. Success requires exit code 0, one valid JSON
// result with status "ok", and valid optional content outputs. The controller
// later verifies that every declared output exists, so a process that reports
// ok without producing a result still fails the stage.
func (e *External) Execute(ctx context.Context, request Request) (Result, error) {
	result := Result{Identity: e.Identity()}
	if err := e.spec.Validate(e.name); err != nil {
		return result, err
	}
	if request.WorkDir == "" {
		return result, errors.New("stage executor: workdir обязателен")
	}
	doc := newRequestDocument(request)
	stdin, err := jsonMarshal(doc)
	if err != nil {
		return result, fmt.Errorf("stage executor: сериализация запроса: %w", err)
	}

	env, secrets := e.buildEnv(request)
	stdout := NewLimitedBuffer(e.spec.maxResult())
	stderr := NewLimitedBuffer(e.spec.maxOutput())
	runCtx, cancel := context.WithTimeout(ctx, e.spec.timeout())
	defer cancel()
	started := time.Now()
	provenance := map[string]string{}
	staged := map[string][]byte{}
	runErr := e.launcher.Run(runCtx, Launch{
		Argv: e.spec.Argv, Dir: request.WorkDir, Env: env, Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Request: request, Provenance: provenance, StagedFiles: staged,
	})
	if len(provenance) > 0 {
		result.Metadata = provenance
	}
	result.LogPath = e.writeLog(request, started, runErr, stdout, stderr, secrets)

	if runErr != nil {
		if ctx.Err() != nil {
			// The surrounding stage ended (cancel or its own deadline): keep
			// the context error so the controller classifies it correctly.
			return result, fmt.Errorf("внешний исполнитель %s прерван: %w", e.name, ctx.Err())
		}
		if runCtx.Err() != nil {
			return result, fmt.Errorf("внешний исполнитель %s превысил таймаут %s: %w", e.name, e.spec.timeout(), ErrTimeout)
		}
		return result, fmt.Errorf("внешний исполнитель %s завершился с ошибкой: %v%s", e.name, runErr, stderrTail(stderr, secrets))
	}
	if stdout.Truncated() {
		return result, fmt.Errorf("внешний исполнитель %s: результат превышает %d байт", e.name, e.spec.maxResult())
	}
	var parsed resultDocument
	if err := strictjson.Unmarshal(stdout.Bytes(), e.spec.maxResult(), &parsed); err != nil {
		return result, fmt.Errorf("внешний исполнитель %s: stdout не является одним валидным JSON-результатом: %w", e.name, err)
	}
	if parsed.SchemaVersion != ProtocolVersion {
		return result, fmt.Errorf("внешний исполнитель %s: schema_version %d не поддерживается (ожидается %d)", e.name, parsed.SchemaVersion, ProtocolVersion)
	}
	metadata, err := sanitizeMetadata(parsed.Metadata, secrets)
	if err != nil {
		return result, fmt.Errorf("внешний исполнитель %s: %w", e.name, err)
	}
	for key, value := range provenance {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata[key] = value
	}
	result.Metadata = metadata
	result.Summary = truncateString(redactText(parsed.Summary, secrets), maxSummaryBytes)
	result.Verdict = strings.TrimSpace(parsed.Verdict)
	switch parsed.Status {
	case "ok":
	case "error":
		reason := truncateString(redactText(parsed.Error, secrets), maxSummaryBytes)
		if reason == "" {
			reason = "причина не указана"
		}
		return result, fmt.Errorf("внешний исполнитель %s сообщил об ошибке: %s", e.name, reason)
	default:
		return result, fmt.Errorf("внешний исполнитель %s: status %q недопустим (ok|error)", e.name, parsed.Status)
	}
	if err := validateContentOutputs(request, parsed.Outputs); err != nil {
		return result, fmt.Errorf("внешний исполнитель %s: %w", e.name, err)
	}
	if err := writeContentOutputs(request, parsed.Outputs); err != nil {
		return result, fmt.Errorf("внешний исполнитель %s: %w", e.name, err)
	}
	if err := writeStagedFiles(request, staged); err != nil {
		return result, fmt.Errorf("внешний исполнитель %s: %w", e.name, err)
	}
	return result, nil
}

func newRequestDocument(request Request) requestDocument {
	doc := requestDocument{
		SchemaVersion: ProtocolVersion, RunID: request.RunID, AttemptID: request.AttemptID,
		Stage: request.Stage, Feature: request.Feature, Task: request.Task, Instructions: request.Instructions,
		WorkDir: request.WorkDir, ArtifactRoot: request.ArtifactRoot, Outputs: request.Outputs,
		Mutation: request.Mutation, AllowedPaths: request.AllowedPaths,
		Inputs: make([]requestInput, 0, len(request.Inputs)),
	}
	if doc.Outputs == nil {
		doc.Outputs = []Output{}
	}
	for _, input := range request.Inputs {
		doc.Inputs = append(doc.Inputs, requestInput{Name: input.Name, Path: input.Path, Size: input.Size})
	}
	return doc
}

// buildEnv returns the process environment and the secret values to redact.
// Only allowlisted variables plus controller-owned AI_TEAM_* values pass.
func (e *External) buildEnv(request Request) (env []string, secrets []string) {
	current := map[string]string{}
	for _, pair := range e.environ() {
		if name, value, ok := strings.Cut(pair, "="); ok {
			current[name] = value
		}
	}
	names := append([]string(nil), e.spec.EnvAllow...)
	sort.Strings(names)
	hasPath := false
	for _, name := range names {
		value, ok := current[name]
		if !ok {
			continue
		}
		if name == "PATH" {
			hasPath = true
		}
		env = append(env, name+"="+value)
		if name != "PATH" && len(value) >= minSecretBytes {
			secrets = append(secrets, value)
		}
	}
	if !hasPath {
		env = append(env, "PATH="+defaultPath)
	}
	env = append(env,
		fmt.Sprintf("AI_TEAM_PROTOCOL=%d", ProtocolVersion),
		"AI_TEAM_RUN_ID="+request.RunID,
		"AI_TEAM_ATTEMPT_ID="+request.AttemptID,
		"AI_TEAM_STAGE="+request.Stage,
	)
	return env, secrets
}

func (e *External) writeLog(request Request, started time.Time, runErr error, stdout, stderr *LimitedBuffer, secrets []string) string {
	if request.LogDir == "" {
		return ""
	}
	if err := os.MkdirAll(request.LogDir, 0o755); err != nil {
		return ""
	}
	name := request.AttemptID
	if name == "" {
		name = request.Stage
	}
	path := filepath.Join(request.LogDir, name+".log")
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "\n===== %s | внешний исполнитель %s | этап %s =====\n", started.Format(time.RFC3339), e.name, request.Stage)
	if runErr != nil {
		fmt.Fprintf(&buffer, "[процесс завершился с ошибкой: %v]\n", runErr)
	}
	buffer.WriteString("--- stderr ---\n")
	buffer.Write(stderr.Bytes())
	if stderr.Truncated() {
		buffer.WriteString("\n[stderr усечён]\n")
	}
	buffer.WriteString("\n--- stdout ---\n")
	buffer.Write(stdout.Bytes())
	if stdout.Truncated() {
		buffer.WriteString("\n[stdout усечён]\n")
	}
	buffer.WriteString("\n")
	cleaned := redact.RedactFile([]byte(redactText(strings.ToValidUTF8(buffer.String(), "?"), secrets)))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }() // best-effort log: the run result is decided independently.
	if _, err := file.Write(cleaned); err != nil {
		return ""
	}
	return path
}

// validateContentOutputs checks every content output before any is written, so
// a response with one bad entry writes nothing.
func validateContentOutputs(request Request, outputs map[string]resultOutput) error {
	declared := make(map[string]bool, len(request.Outputs))
	for _, output := range request.Outputs {
		declared[output.Name] = true
	}
	for name, output := range outputs {
		if !declared[name] {
			return fmt.Errorf("результат содержит необъявленный output %q", name)
		}
		if output.Content == nil {
			return fmt.Errorf("output %q: ожидается content", name)
		}
	}
	return nil
}

func writeContentOutputs(request Request, outputs map[string]resultOutput) error {
	declared := make(map[string]string, len(request.Outputs))
	for _, output := range request.Outputs {
		declared[output.Name] = output.Path
	}
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path, ok := declared[name]
		if !ok {
			return fmt.Errorf("результат содержит необъявленный output %q", name)
		}
		content := outputs[name].Content
		if content == nil {
			return fmt.Errorf("output %q: ожидается content", name)
		}
		if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
			return fmt.Errorf("output %q: каталог: %w", name, err)
		}
		if err := safeio.ReplaceRegularFileNoFollow(path, []byte(*content), 0o644); err != nil {
			return fmt.Errorf("output %q: запись: %w", name, err)
		}
	}
	return nil
}

func sanitizeMetadata(metadata map[string]string, secrets []string) (map[string]string, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	if len(metadata) > maxMetadataEntries {
		return nil, fmt.Errorf("metadata содержит %d записей (максимум %d)", len(metadata), maxMetadataEntries)
	}
	cleaned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if !metadataKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("metadata: невалидный ключ %q", key)
		}
		if len(value) > maxMetadataValueBytes {
			return nil, fmt.Errorf("metadata %q: значение длиннее %d байт", key, maxMetadataValueBytes)
		}
		if redact.ClassifyField(key) == redact.FieldSecret {
			cleaned[key] = "[redacted]"
			continue
		}
		cleaned[key] = redactText(strings.ToValidUTF8(value, "?"), secrets)
	}
	return cleaned, nil
}

func stderrTail(stderr *LimitedBuffer, secrets []string) string {
	text := strings.Join(strings.Fields(strings.ToValidUTF8(string(stderr.Bytes()), "?")), " ")
	if text == "" {
		return ""
	}
	if len(text) > 512 {
		text = "…" + strings.ToValidUTF8(text[len(text)-512:], "")
	}
	return "; stderr: " + string(redact.RedactFile([]byte(redactText(text, secrets))))
}

func redactText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func truncateString(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}
