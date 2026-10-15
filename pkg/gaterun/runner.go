package gaterun

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/arturpanteleev/ai-team/pkg/redact"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Request is one gate invocation. Attempt carries the identity of the attempt
// (task, stage, visit, artifact hash, gate, name, mode, group, attempt ID);
// its outcome fields are ignored and filled in by the runner.
type Request struct {
	Attempt  workflow.GateAttempt
	Config   Config
	Artifact ArtifactInfo
	// RepoRoot is the repository root; cwd and relative program paths are
	// resolved against it.
	RepoRoot string
}

// Runner runs gate programs and records attempts under EvidenceRoot.
//
// Layout (everything below EvidenceRoot/<attempt_id>/):
//
//	attempt.sig       HMAC of attempt.json under the key in EvidenceRoot
//	attempt.json      the immutable attempt record, written last
//	stdout.log        redacted stdout (the result document)
//	stderr.log        redacted stderr
//	invocation.json   redacted record of how the program was run
//	evidence/...      evidence files named by the program, verified and scanned
//
// The program never sees the attempt directory: its evidence_dir is a fresh
// private directory under WorkRoot, removed after the run. A program that
// nevertheless finds and writes into the attempt directory makes the attempt
// an error, and attempt.json is trusted on reload only when its HMAC (key
// kept in EvidenceRoot/.gaterun.key, never passed to the program) matches.
// The host launcher runs the program as the same user, so the key file is
// readable by it: this protects against writes into the attempt directory
// during the run, accidental corruption and processes without access to the
// key, not against a hostile program. That needs isolation (container
// launcher, separate uid).
type Runner struct {
	// EvidenceRoot is an existing directory the runner owns.
	EvidenceRoot string
	// WorkRoot is the parent of the programs' private evidence directories;
	// it must be outside EvidenceRoot. Defaults to os.TempDir().
	WorkRoot string
	// Launcher defaults to HostLauncher.
	Launcher Launcher
	// DockerLauncher runs gates whose Config has Docker settings; defaults to
	// DockerLauncher{}. A docker gate never falls back to the host launcher.
	DockerLauncher Launcher
	// LookupEnv reads the controller environment for env_allow; defaults to
	// os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Now defaults to time.Now.
	Now func() time.Time

	mu    sync.Mutex
	locks map[string]*keyLock

	sweepOnce sync.Once

	keyMu   sync.Mutex
	key     []byte
	keyRoot string
}

// OrphanSweeper is implemented by launchers that can clean up after a dead
// controller.
type OrphanSweeper interface {
	SweepOrphans(ctx context.Context) ([]string, error)
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (r *Runner) lock(id string) func() {
	r.mu.Lock()
	if r.locks == nil {
		r.locks = map[string]*keyLock{}
	}
	entry := r.locks[id]
	if entry == nil {
		entry = &keyLock{}
		r.locks[id] = entry
	}
	entry.refs++
	r.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(r.locks, id)
		}
		r.mu.Unlock()
	}
}

// Run executes the request and returns the recorded attempt.
//
// Run is idempotent by attempt ID: when attempt.json already exists for the ID
// the stored attempt is returned without starting the program, provided the
// identity matches; the same ID with a different task, stage, visit, artifact
// hash or gate is an error (an ID must never be reused for another check).
// Concurrent calls with one ID run the program once. That guarantee rests on
// an in-process mutex: two controller processes sharing one EvidenceRoot are
// not coordinated and may both run the program for the same ID (the second
// commit then fails because attempt.json is immutable). A deployment runs one
// controller per EvidenceRoot.
//
// A returned error means the request was invalid or the controller could not
// store the record; the gate has then not produced an attempt. Everything the
// gate itself does — verdicts, crashes, timeouts, cancellation, protocol
// violations — is expressed in the returned attempt's Execution and Verdict.
func (r *Runner) Run(ctx context.Context, req Request) (workflow.GateAttempt, error) {
	launcher := r.Launcher
	if launcher == nil {
		launcher = HostLauncher{}
	}
	if req.Config.Docker != nil {
		launcher = r.DockerLauncher
		if launcher == nil {
			launcher = DockerLauncher{}
		}
		// On the first Docker gate of this Runner, remove containers that a
		// previous controller left behind when it died. Best effort: a failure
		// never blocks the gate.
		if sweeper, ok := launcher.(OrphanSweeper); ok {
			r.sweepOnce.Do(func() { _, _ = sweeper.SweepOrphans(ctx) })
		}
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	lookup := r.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := req.Config.Validate(); err != nil {
		return workflow.GateAttempt{}, err
	}
	base := req.Attempt
	base.Executor = launcher.Kind()
	base.Mode = base.Mode.Effective()
	base.Execution, base.Verdict, base.Reason, base.Evidence = workflow.GateExecutionRunning, "", "", nil
	base.StartedAt, base.FinishedAt = now().UTC(), time.Time{}
	if err := base.Validate(); err != nil {
		return workflow.GateAttempt{}, err
	}
	if !ValidAttemptID(base.AttemptID) {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: attempt_id %q is not a safe identifier", base.AttemptID)
	}
	if _, err := safeio.ExistingDir(r.EvidenceRoot); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: evidence root: %w", err)
	}
	if _, err := safeio.ExistingDir(req.RepoRoot); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: repo root: %w", err)
	}

	// Create the key before any program of this Runner can run, so a program
	// cannot plant its own key on the first run.
	if _, err := r.macKey(); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: signing key: %w", err)
	}

	unlock := r.lock(base.AttemptID)
	defer unlock()

	if stored, found, err := r.loadStored(base); err != nil || found {
		return stored, err
	}
	return r.execute(ctx, launcher, lookup, now, req, base)
}

func (r *Runner) attemptDir(id string) string {
	root, _ := filepath.Abs(r.EvidenceRoot)
	return filepath.Join(root, id)
}

// loadStored returns the committed attempt for the ID, if any, and removes the
// leftovers of an attempt that never committed (the controller died mid-run).
func (r *Runner) loadStored(identity workflow.GateAttempt) (workflow.GateAttempt, bool, error) {
	dir := r.attemptDir(identity.AttemptID)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return workflow.GateAttempt{}, false, nil
	}
	if err != nil {
		return workflow.GateAttempt{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: %s must be a directory without symlink", dir)
	}
	data, err := safeio.ReadRegularFile(filepath.Join(dir, "attempt.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: cleaning uncommitted attempt: %w", rmErr)
		}
		return workflow.GateAttempt{}, false, nil
	}
	if err != nil {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: reading stored attempt: %w", err)
	}
	if err := r.verifyMAC(dir, identity.AttemptID, data); err != nil {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: stored attempt %s failed its integrity check: %w", identity.AttemptID, err)
	}
	var stored workflow.GateAttempt
	if err := json.Unmarshal(data, &stored); err != nil {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: stored attempt %s is corrupt: %w", identity.AttemptID, err)
	}
	if err := stored.Validate(); err != nil || !stored.Execution.Terminal() {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: stored attempt %s is not a finished valid record", identity.AttemptID)
	}
	if !sameIdentity(stored, identity) {
		return workflow.GateAttempt{}, false, fmt.Errorf("gaterun: attempt_id %s already belongs to a different check (task, stage, visit, artifact hash or gate differ)", identity.AttemptID)
	}
	return stored, true, nil
}

func sameIdentity(a, b workflow.GateAttempt) bool {
	return a.TaskID == b.TaskID && a.Stage == b.Stage && a.Visit == b.Visit &&
		a.ArtifactSHA256 == b.ArtifactSHA256 && a.Gate == b.Gate &&
		a.GateVersion == b.GateVersion && a.GateName == b.GateName && a.Group == b.Group
}

func (r *Runner) execute(ctx context.Context, launcher Launcher, lookup func(string) (string, bool), now func() time.Time, req Request, attempt workflow.GateAttempt) (workflow.GateAttempt, error) {
	cfg := req.Config
	id := attempt.AttemptID
	dir, err := safeio.EnsureDir(r.EvidenceRoot, id)
	if err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: attempt directory: %w", err)
	}
	workRoot := r.WorkRoot
	if workRoot == "" {
		workRoot = os.TempDir()
	}
	workDir, err := os.MkdirTemp(workRoot, "ai-team-gate-work-")
	if err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	cwd, err := safeio.ExistingDir(req.RepoRoot, cwdComponents(cfg.CWD)...)
	if err != nil {
		return r.finish(dir, attempt, now, nil, fmt.Errorf("working directory %q is missing or unsafe", cfg.CWD), workflow.GateExecutionError, "")
	}
	repoRoot, _ := filepath.Abs(req.RepoRoot)

	// Environment: only the reserved protocol variables and the allowlist.
	env := []string{"AI_TEAM_GATE_PROTOCOL=" + Protocol, "AI_TEAM_GATE_ATTEMPT_ID=" + id}
	var secrets []string
	for _, name := range cfg.EnvAllow {
		value, ok := lookup(name)
		if !ok {
			continue
		}
		env = append(env, name+"="+value)
		secrets = append(secrets, value)
	}
	clean := newSanitizer(secrets)

	input := Input{
		Protocol: Protocol, TaskID: attempt.TaskID, Stage: attempt.Stage, Visit: attempt.Visit,
		AttemptID: id,
		Gate:      GateInfo{ID: attempt.Gate, Version: attempt.GateVersion, Name: attempt.GateName},
		Artifact:  req.Artifact,
		Paths:     PathsInfo{RepoRoot: repoRoot, EvidenceDir: workDir},
		Limits:    LimitsInfo{TimeoutSeconds: int(cfg.Timeout / time.Second), MaxOutputBytes: cfg.MaxOutputBytes},
	}
	if err := input.Validate(); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: %w", err)
	}
	stdin, err := json.Marshal(input)
	if err != nil {
		return workflow.GateAttempt{}, err
	}

	// Config.Validate guarantees a relative program path stays lexically inside
	// the repository. Symlinks inside the repository are not resolved: the
	// repository content is part of the trusted configuration.
	argv := append([]string{}, cfg.Command...)
	if cfg.Docker == nil && strings.Contains(argv[0], "/") && !filepath.IsAbs(argv[0]) {
		argv[0] = filepath.Join(repoRoot, filepath.FromSlash(argv[0]))
	}

	var outcome Outcome
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	if ctx.Err() != nil {
		outcome = Outcome{ExitCode: -1, Err: ctx.Err()}
	} else {
		outcome = launcher.Launch(runCtx, LaunchSpec{Argv: argv, Dir: cwd, Env: env, Stdin: stdin, MaxOutputBytes: cfg.MaxOutputBytes, Input: input, Docker: cfg.Docker})
	}
	timedOut := runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()

	execution, result, reasonErr := classify(ctx, timedOut, outcome, cfg)
	if reasonErr != nil && outcome.Detail != "" {
		reasonErr = fmt.Errorf("%v; %s", reasonErr, outcome.Detail)
	}
	// The attempt directory was empty and its path was never given to the
	// program. Anything in it now was put there by the program: discard it
	// and refuse the result, whatever the program printed.
	if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) > 0 {
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
		execution, reasonErr = workflow.GateExecutionError, errors.New("the program wrote into the controller's attempt directory; result discarded")
	}
	var evidence []workflow.GateEvidenceRef
	if execution == workflow.GateExecutionSucceeded {
		var evErr error
		evidence, evErr = collectEvidence(dir, workDir, id, result.Evidence, clean)
		if evErr != nil {
			execution, reasonErr = workflow.GateExecutionError, evErr
		}
	}
	extra := r.writeRecords(dir, id, cfg, launcher.Kind(), argv, env, outcome, clean)
	if extra.err != nil {
		return workflow.GateAttempt{}, extra.err
	}
	verdict := workflow.GateVerdict("")
	reason := ""
	if execution == workflow.GateExecutionSucceeded {
		verdict = workflow.GateVerdict(result.Verdict)
		reason = clean.text(result.Reason)
	} else {
		reason = clean.text(reasonErr.Error())
		if execution == workflow.GateExecutionError {
			if tail := stderrTail(outcome.Stderr); tail != "" {
				reason += "; stderr: " + clean.text(tail)
			}
		}
	}
	evidence = append(extra.refs, evidence...)
	// The program's raw output dir is removed on return: only verified copies are kept.
	return r.finish(dir, attempt, now, evidence, errors.New(reason), execution, verdict)
}

// classify maps what happened to the execution state. Only an on-time clean
// exit with exactly one valid result document is "succeeded"; every other end
// is a technical state, never a verdict.
func classify(ctx context.Context, timedOut bool, outcome Outcome, cfg Config) (workflow.GateExecution, Result, error) {
	switch {
	case outcome.OutputExceeded:
		return workflow.GateExecutionError, Result{}, fmt.Errorf("output limit of %d bytes per stream exceeded; process killed", cfg.MaxOutputBytes)
	case outcome.Err != nil && ctx.Err() != nil:
		return workflow.GateExecutionCanceled, Result{}, errors.New("canceled before the gate finished; process group killed")
	case outcome.Err != nil && timedOut:
		return workflow.GateExecutionTimeout, Result{}, fmt.Errorf("timed out after %s; process group killed", cfg.Timeout)
	case outcome.Err != nil && outcome.ExitCode > 0:
		return workflow.GateExecutionError, Result{}, fmt.Errorf("process exited with code %d", outcome.ExitCode)
	case outcome.Err != nil:
		return workflow.GateExecutionError, Result{}, fmt.Errorf("process lost or not started: %v", outcome.Err)
	}
	result, err := ParseResult(outcome.Stdout, cfg.MaxOutputBytes)
	if err != nil {
		return workflow.GateExecutionError, Result{}, fmt.Errorf("stdout is not a valid gate result: %v", err)
	}
	return workflow.GateExecutionSucceeded, result, nil
}

func cwdComponents(cwd string) []string {
	var parts []string
	for _, part := range strings.Split(cwd, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func stderrTail(stderr []byte) string {
	text := strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, string(stderr))
	text = strings.Join(strings.Fields(text), " ")
	const max = 200
	if len(text) > max {
		text = "..." + text[len(text)-max:]
	}
	return text
}

// collectEvidence copies each evidence file named by the program from its work
// directory into the attempt's evidence directory after proving it is a plain
// text file inside the work directory, within the size limit, matching its
// declared digest and free of secrets. A file that fails any check makes the
// whole attempt an error: evidence is never silently dropped or rewritten.
func collectEvidence(attemptDir, workDir, id string, refs []EvidenceRef, clean sanitizer) ([]workflow.GateEvidenceRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if err := safeio.ValidateTree(workDir); err != nil {
		return nil, fmt.Errorf("evidence directory is unsafe: %v", err)
	}
	var stored []workflow.GateEvidenceRef
	for _, ref := range refs {
		data, err := safeio.ReadRegularFile(filepath.Join(workDir, filepath.FromSlash(ref.Path)), MaxEvidenceBytes)
		if err != nil {
			return nil, fmt.Errorf("evidence %q is unreadable or too large: %v", ref.Path, err)
		}
		digest := hashHex(data)
		if ref.SHA256 != "" && ref.SHA256 != digest {
			return nil, fmt.Errorf("evidence %q does not match its declared sha256", ref.Path)
		}
		if redact.IsBinary(data) {
			return nil, fmt.Errorf("evidence %q is binary; only text evidence can be scanned for secrets", ref.Path)
		}
		if clean.leaks(data) {
			return nil, fmt.Errorf("evidence %q contains a secret and was not stored", ref.Path)
		}
		parts := append([]string{"evidence"}, strings.Split(ref.Path, "/")...)
		target, err := safeio.EnsureDir(attemptDir, parts[:len(parts)-1]...)
		if err != nil {
			return nil, fmt.Errorf("evidence directory: %v", err)
		}
		if err := safeio.WriteRegularFileNoFollow(filepath.Join(target, parts[len(parts)-1]), data, safeio.ReadOnlyFileMode); err != nil {
			return nil, fmt.Errorf("storing evidence %q: %v", ref.Path, err)
		}
		stored = append(stored, workflow.GateEvidenceRef{Kind: ref.Kind, Path: id + "/" + strings.Join(parts, "/"), SHA256: digest})
	}
	return stored, nil
}

type records struct {
	refs []workflow.GateEvidenceRef
	err  error
}

// writeRecords stores the redacted logs and the invocation record.
func (r *Runner) writeRecords(dir, id string, cfg Config, kind workflow.GateKind, argv, env []string, outcome Outcome, clean sanitizer) records {
	var refs []workflow.GateEvidenceRef
	put := func(name, kind string, data []byte) error {
		if err := safeio.WriteRegularFileNoFollow(filepath.Join(dir, name), data, safeio.ReadOnlyFileMode); err != nil {
			return fmt.Errorf("gaterun: storing %s: %w", name, err)
		}
		refs = append(refs, workflow.GateEvidenceRef{Kind: kind, Path: id + "/" + name, SHA256: hashHex(data)})
		return nil
	}
	names := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	redactedArgv := make([]string, len(argv))
	for i, arg := range argv {
		redactedArgv[i] = clean.text(arg)
	}
	record := map[string]any{
		"protocol":         Protocol,
		"executor":         string(kind),
		"argv":             redactedArgv,
		"cwd":              cfg.CWD,
		"env_names":        names,
		"timeout_seconds":  int(cfg.Timeout / time.Second),
		"max_output_bytes": cfg.MaxOutputBytes,
		"exit_code":        outcome.ExitCode,
		"output_exceeded":  outcome.OutputExceeded,
	}
	if len(outcome.Record) > 0 {
		record["launcher"] = cleanRecord(outcome.Record, clean)
	}
	invocation, _ := json.MarshalIndent(record, "", "  ")
	for _, item := range []struct {
		name, kind string
		data       []byte
	}{
		{"stdout.log", "log", clean.bytes(outcome.Stdout)},
		{"stderr.log", "log", clean.bytes(outcome.Stderr)},
		{"invocation.json", "invocation", invocation},
	} {
		if err := put(item.name, item.kind, item.data); err != nil {
			return records{err: err}
		}
	}
	return records{refs: refs}
}

// finish stamps the terminal state, validates and commits attempt.json. The
// reason in reasonErr is already sanitized by the caller.
func (r *Runner) finish(dir string, attempt workflow.GateAttempt, now func() time.Time, evidence []workflow.GateEvidenceRef, reasonErr error, execution workflow.GateExecution, verdict workflow.GateVerdict) (workflow.GateAttempt, error) {
	attempt.Execution = execution
	attempt.Verdict = verdict
	attempt.Evidence = evidence
	if reasonErr != nil {
		attempt.Reason = reasonErr.Error()
	}
	attempt.FinishedAt = now().UTC()
	if !attempt.FinishedAt.After(attempt.StartedAt) {
		attempt.FinishedAt = attempt.StartedAt
	}
	if err := attempt.Validate(); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: %w", err)
	}
	data, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return workflow.GateAttempt{}, err
	}
	mac, err := r.macOf(attempt.AttemptID, data)
	if err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: signing attempt: %w", err)
	}
	if err := safeio.WriteRegularFileNoFollow(filepath.Join(dir, "attempt.sig"), []byte(mac), safeio.ReadOnlyFileMode); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: committing attempt: %w", err)
	}
	if err := safeio.WriteRegularFileNoFollow(filepath.Join(dir, "attempt.json"), data, safeio.ReadOnlyFileMode); err != nil {
		return workflow.GateAttempt{}, fmt.Errorf("gaterun: committing attempt: %w", err)
	}
	return attempt, nil
}

const (
	keyFile = ".gaterun.key"
	keyLen  = 32
)

// macKey returns the HMAC key of the evidence root, creating it if absent.
// The name starts with a dot, which no attempt ID may. Creation is atomic: the
// key is written to a private temporary file and published with a hard link,
// which fails if the name exists, so no reader ever sees a partial key and
// concurrent creators (goroutines, Runners, processes) converge on one key.
// A key of the wrong length is an error, never silently replaced or used.
func (r *Runner) macKey() ([]byte, error) {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	if r.keyRoot == r.EvidenceRoot && len(r.key) == keyLen {
		return r.key, nil
	}
	path := filepath.Join(r.EvidenceRoot, keyFile)
	for range 3 {
		key, err := safeio.ReadRegularFile(path, 1024)
		if err == nil {
			if len(key) != keyLen {
				return nil, fmt.Errorf("gaterun: key file %s has %d bytes, want %d", path, len(key), keyLen)
			}
			r.key, r.keyRoot = key, r.EvidenceRoot
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := publishKey(r.EvidenceRoot, path); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("gaterun: cannot create the key file")
}

// publishKey creates the key file unless it already exists.
func publishKey(root, path string) error {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(root, keyFile+".tmp-"+hex.EncodeToString(suffix[:]))
	if err := safeio.WriteRegularFileNoFollow(tmp, key, 0o400); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func (r *Runner) macOf(id string, data []byte) (string, error) {
	key, err := r.macKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "\n"))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (r *Runner) verifyMAC(dir, id string, data []byte) error {
	stored, err := safeio.ReadRegularFile(filepath.Join(dir, "attempt.sig"), 1024)
	if err != nil {
		return fmt.Errorf("signature unreadable: %w", err)
	}
	want, err := r.macOf(id, data)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(strings.TrimSpace(string(stored))), []byte(want)) {
		return errors.New("signature does not match")
	}
	return nil
}

// cleanRecord passes a launcher record through the sanitizer value by value,
// so a secret that slipped into any string is masked and the JSON stays valid.
func cleanRecord(record map[string]any, clean sanitizer) any {
	data, err := json.Marshal(record)
	if err != nil {
		return map[string]any{"error": "launcher record could not be encoded"}
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return map[string]any{"error": "launcher record could not be decoded"}
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			return clean.text(v)
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
			return v
		case map[string]any:
			for key := range v {
				v[key] = walk(v[key])
			}
			return v
		}
		return value
	}
	return walk(generic)
}
