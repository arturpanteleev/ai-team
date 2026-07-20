// Package checks executes deterministic verification commands without a shell.
package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/process"
	"gopkg.in/yaml.v3"
)

const maxCapturedOutput = 256 << 10 // 256 KiB per stream
const maxStructuredOutput = 64 << 20

const (
	PolicyRequired = "required"
	PolicyOptional = "optional"
	AdapterCommand = "command"
	AdapterGoTest  = "go-test-json"

	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

var classes = map[string]bool{
	"formatter": true, "lint": true, "build": true, "unit": true,
	"integration": true, "e2e": true, "coverage": true, "race": true,
	"security": true,
}

type Definition struct {
	Name       string   `yaml:"name" json:"name"`
	Class      string   `yaml:"class" json:"class"`
	Adapter    string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Command    []string `yaml:"command" json:"command"`
	Policy     string   `yaml:"policy" json:"policy"`
	Timeout    string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	WorkingDir string   `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
}

func (d *Definition) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("check definition должна быть mapping")
	}
	allowed := map[string]bool{
		"name": true, "class": true, "adapter": true, "command": true, "policy": true,
		"timeout": true, "working_dir": true,
	}
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return fmt.Errorf("check definition: duplicate field %q", key)
		}
		seen[key] = true
		if !allowed[key] {
			return fmt.Errorf("check definition: unknown field %q", key)
		}
	}
	type plain Definition
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*d = Definition(decoded)
	return d.Validate()
}

func (d Definition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("check name обязателен")
	}
	if !classes[d.Class] {
		return fmt.Errorf("check %s: неизвестный class %q", d.Name, d.Class)
	}
	if len(d.Command) == 0 || d.Command[0] == "" {
		return fmt.Errorf("check %s: command обязателен", d.Name)
	}
	adapter := d.NormalizedAdapter()
	if adapter != AdapterCommand && adapter != AdapterGoTest {
		return fmt.Errorf("check %s: неизвестный adapter %q", d.Name, d.Adapter)
	}
	if adapter == AdapterGoTest {
		if d.Command[0] != "go" || len(d.Command) < 3 || d.Command[1] != "test" || !containsArgument(d.Command[2:], "-json") {
			return fmt.Errorf("check %s: adapter %s требует command [go, test, -json, ...]", d.Name, AdapterGoTest)
		}
		if d.Class != "unit" && d.Class != "integration" && d.Class != "e2e" {
			return fmt.Errorf("check %s: adapter %s допустим только для test class", d.Name, AdapterGoTest)
		}
	}
	if d.Policy != PolicyRequired && d.Policy != PolicyOptional {
		return fmt.Errorf("check %s: policy должен быть required или optional", d.Name)
	}
	if d.Timeout != "" {
		if duration, err := time.ParseDuration(d.Timeout); err != nil || duration <= 0 {
			return fmt.Errorf("check %s: невалидный timeout %q", d.Name, d.Timeout)
		}
	}
	if d.WorkingDir != "" {
		cleaned := filepath.Clean(filepath.FromSlash(d.WorkingDir))
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("check %s: working_dir должен оставаться внутри target", d.Name)
		}
	}
	return nil
}

func (d Definition) NormalizedAdapter() string {
	if d.Adapter == "" {
		return AdapterCommand
	}
	return d.Adapter
}

type Result struct {
	Name                   string        `json:"name"`
	Class                  string        `json:"class"`
	Adapter                string        `json:"adapter"`
	Command                []string      `json:"command"`
	Policy                 string        `json:"policy"`
	WorkingDir             string        `json:"working_dir"`
	ToolPath               string        `json:"tool_path,omitempty"`
	ToolVersion            string        `json:"tool_version,omitempty"`
	StartedAt              time.Time     `json:"started_at"`
	FinishedAt             time.Time     `json:"finished_at"`
	Duration               time.Duration `json:"duration_ns"`
	ExitCode               int           `json:"exit_code"`
	Status                 string        `json:"status"`
	Stdout                 string        `json:"stdout,omitempty"`
	Stderr                 string        `json:"stderr,omitempty"`
	Reason                 string        `json:"reason,omitempty"`
	Truncated              bool          `json:"truncated,omitempty"`
	WorkspaceDigestBefore  string        `json:"workspace_digest_before,omitempty"`
	WorkspaceDigestAfter   string        `json:"workspace_digest_after,omitempty"`
	EvidenceDigest         string        `json:"evidence_digest,omitempty"`
	DiscoveredTests        int           `json:"discovered_tests,omitempty"`
	PassedTests            int           `json:"passed_tests,omitempty"`
	StructuredOutputBytes  int64         `json:"structured_output_bytes,omitempty"`
	StructuredOutputSHA256 string        `json:"structured_output_sha256,omitempty"`
}

type RequiredFailureError struct {
	Check string
	Cause string
}

func (e *RequiredFailureError) Error() string {
	return fmt.Sprintf("required check %s failed: %s", e.Check, e.Cause)
}

type Runner struct {
	TargetDir string
}

func (r Runner) RunAll(ctx context.Context, definitions []Definition) ([]Result, error) {
	results := make([]Result, 0, len(definitions))
	var requiredErrors []error
	for _, definition := range definitions {
		result, err := r.Run(ctx, definition)
		results = append(results, result)
		if err != nil {
			requiredErrors = append(requiredErrors, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return results, errors.Join(requiredErrors...)
}

func (r Runner) Run(ctx context.Context, definition Definition) (result Result, returnErr error) {
	result = Result{
		Name: definition.Name, Class: definition.Class,
		Adapter: definition.NormalizedAdapter(),
		Command: append([]string(nil), definition.Command...), Policy: definition.Policy,
		ExitCode: -1,
	}
	if err := definition.Validate(); err != nil {
		result.Status, result.Reason = StatusFailed, err.Error()
		return result, err
	}
	workingDir, err := confinedWorkingDir(r.TargetDir, definition.WorkingDir)
	if err != nil {
		result.Status, result.Reason = StatusFailed, err.Error()
		return result, err
	}
	result.WorkingDir = workingDir
	result.WorkspaceDigestBefore, err = workspaceDigest(r.TargetDir)
	if err != nil {
		result.Status, result.Reason = StatusFailed, "workspace baseline: "+err.Error()
		return result, err
	}
	defer func() {
		after, digestErr := workspaceDigest(r.TargetDir)
		if digestErr != nil {
			result.Status = StatusFailed
			result.Reason = "workspace verification: " + digestErr.Error()
			returnErr = errors.Join(returnErr, digestErr)
		} else {
			result.WorkspaceDigestAfter = after
			if result.WorkspaceDigestBefore != after {
				result.Status = StatusFailed
				result.Reason = "check изменил workspace; verification commands должны быть read-only"
				returnErr = errors.Join(returnErr, &RequiredFailureError{Check: definition.Name, Cause: result.Reason})
			}
		}
		result.EvidenceDigest = resultDigest(result)
	}()
	toolPath, err := resolveToolPath(definition.Command[0], workingDir)
	if err != nil {
		result.Reason = fmt.Sprintf("tool %s unavailable", definition.Command[0])
		if definition.Policy == PolicyOptional {
			result.Status = StatusSkipped
			return result, nil
		}
		result.Status = StatusFailed
		return result, &RequiredFailureError{Check: definition.Name, Cause: result.Reason}
	}
	result.ToolPath = toolPath
	result.ToolVersion = toolFingerprint(toolPath)

	checkCtx := ctx
	if definition.Timeout != "" {
		duration, _ := time.ParseDuration(definition.Timeout)
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	stdout, stderr := &limitedBuffer{limit: maxCapturedOutput}, &limitedBuffer{limit: maxCapturedOutput}
	var structured *goTestCollector
	var stdoutWriter io.Writer = stdout
	if definition.NormalizedAdapter() == AdapterGoTest {
		structured = newGoTestCollector()
		stdoutWriter = io.MultiWriter(stdout, structured)
	}
	command := exec.Command(toolPath, definition.Command[1:]...)
	command.Dir = workingDir
	command.Stdout, command.Stderr = stdoutWriter, stderr
	result.StartedAt = time.Now().UTC()
	runErr := process.Run(checkCtx, command)
	result.FinishedAt = time.Now().UTC()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	result.Truncated = stdout.truncated || stderr.truncated
	if runErr == nil {
		result.ExitCode = 0
		if evidenceErr := validateStructuredEvidence(definition, &result, structured); evidenceErr != nil {
			result.Status, result.Reason = StatusFailed, evidenceErr.Error()
			if definition.Policy == PolicyRequired {
				return result, &RequiredFailureError{Check: definition.Name, Cause: result.Reason}
			}
			return result, nil
		}
		result.Status = StatusPassed
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	result.Status = StatusFailed
	switch {
	case errors.Is(checkCtx.Err(), context.DeadlineExceeded):
		result.Reason = "timeout: " + definition.Timeout
	case errors.Is(checkCtx.Err(), context.Canceled):
		result.Reason = "canceled"
	default:
		result.Reason = runErr.Error()
	}
	if definition.Policy == PolicyRequired {
		return result, &RequiredFailureError{Check: definition.Name, Cause: result.Reason}
	}
	return result, nil
}

func resolveToolPath(command, workingDir string) (string, error) {
	if !strings.ContainsAny(command, `/\`) {
		return exec.LookPath(command)
	}
	path := filepath.FromSlash(command)
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("tool %s не является regular file", path)
	}
	return path, nil
}

// IsTestEvidence prevents a successful arbitrary command from being promoted
// to delivery evidence merely by labelling it as class: unit.
func IsTestEvidence(result Result) bool {
	return result.Status == StatusPassed && result.Policy == PolicyRequired &&
		(result.Class == "unit" || result.Class == "integration" || result.Class == "e2e") &&
		result.Adapter != AdapterCommand && result.DiscoveredTests > 0 && result.PassedTests > 0
}

func validateStructuredEvidence(definition Definition, result *Result, collector *goTestCollector) error {
	switch definition.NormalizedAdapter() {
	case AdapterCommand:
		return nil
	case AdapterGoTest:
		if collector == nil {
			return fmt.Errorf("go test JSON collector отсутствует")
		}
		return collector.Finish(result)
	default:
		return fmt.Errorf("unsupported adapter %q", definition.Adapter)
	}
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type goTestCollector struct {
	pending    []byte
	total      int64
	hash       hash.Hash
	discovered map[string]bool
	passed     map[string]bool
	err        error
}

func newGoTestCollector() *goTestCollector {
	return &goTestCollector{hash: sha256.New(), discovered: make(map[string]bool), passed: make(map[string]bool)}
}

func (c *goTestCollector) Write(data []byte) (int, error) {
	original := len(data)
	c.total += int64(original)
	_, _ = c.hash.Write(data)
	if c.err != nil {
		return original, nil
	}
	if c.total > maxStructuredOutput {
		c.err = fmt.Errorf("go test structured output exceeds %d bytes", maxStructuredOutput)
		return original, nil
	}
	c.pending = append(c.pending, data...)
	for {
		newline := bytes.IndexByte(c.pending, '\n')
		if newline < 0 {
			if len(c.pending) > 1<<20 {
				c.err = fmt.Errorf("go test JSON event exceeds 1 MiB")
			}
			return original, nil
		}
		line := bytes.TrimSpace(c.pending[:newline])
		c.pending = c.pending[newline+1:]
		if len(line) > 0 {
			c.consume(line)
		}
		if c.err != nil {
			return original, nil
		}
	}
}

func (c *goTestCollector) consume(line []byte) {
	var current goTestEvent
	if err := json.Unmarshal(line, &current); err != nil {
		c.err = fmt.Errorf("invalid go test JSON evidence: %w", err)
		return
	}
	if current.Test == "" {
		return
	}
	key := current.Package + "\x00" + current.Test
	switch current.Action {
	case "run", "pass", "fail", "skip":
		c.discovered[key] = true
	}
	if current.Action == "pass" {
		c.passed[key] = true
	}
}

func (c *goTestCollector) Finish(result *Result) error {
	if len(bytes.TrimSpace(c.pending)) > 0 && c.err == nil {
		c.consume(bytes.TrimSpace(c.pending))
	}
	result.StructuredOutputBytes = c.total
	result.StructuredOutputSHA256 = hex.EncodeToString(c.hash.Sum(nil))
	result.DiscoveredTests = len(c.discovered)
	result.PassedTests = len(c.passed)
	if c.err != nil {
		return c.err
	}
	if result.DiscoveredTests == 0 || result.PassedTests == 0 {
		return fmt.Errorf("go test не обнаружил ни одного успешно выполненного test case")
	}
	return nil
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func confinedWorkingDir(target, relative string) (string, error) {
	root, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}
	workingDir := root
	if relative != "" {
		workingDir = filepath.Join(root, filepath.FromSlash(relative))
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return "", err
	}
	workingDir, err = filepath.EvalSymlinks(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if workingDir != root && !strings.HasPrefix(workingDir, root+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory %s вне target", workingDir)
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %s не является каталогом", workingDir)
	}
	return workingDir, nil
}

func toolFingerprint(toolPath string) string {
	file, err := os.Open(toolPath)
	if err != nil {
		return "unavailable"
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unavailable"
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func resultDigest(result Result) string {
	result.EvidenceDigest = ""
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func VerifyResultDigest(result Result) bool {
	return result.EvidenceDigest != "" && result.EvidenceDigest == resultDigest(result)
}

// WorkspaceDigest is the canonical source-tree digest used to bind checks to
// delivery. Controller metadata and Git internals are excluded.
func WorkspaceDigest(target string) (string, error) { return workspaceDigest(target) }

func workspaceDigest(target string) (string, error) {
	root, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("workspace root должен быть обычным каталогом без symlink")
	}
	files := make(map[string]string)
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		first := strings.SplitN(relative, "/", 2)[0]
		if entry.IsDir() && (first == ".git" || first == ".ai-team") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		hash := sha256.New()
		fmt.Fprintf(hash, "mode\x00%d\x00", info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(current)
			if linkErr != nil {
				return linkErr
			}
			_, _ = io.WriteString(hash, target)
		} else if info.Mode().IsRegular() {
			file, openErr := os.Open(current)
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		files[relative] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for relative := range files {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		fmt.Fprintf(hash, "%s\x00%s\x00", relative, files[relative])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
