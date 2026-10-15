package workflow

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Gate contract (S02).
//
// A gate checks exactly one immutable version of a stage result and produces
// a structured outcome with evidence. A gate never chooses the next stage: the
// controller moves the graph from the configured routes and the verified
// outcomes. This file holds only domain types and pure validation; the library
// of named definitions lives in pkg/gatelib and the template schema in
// pkg/config.
//
// Three different things are deliberately kept apart:
//
//   - GateDefinition — what a gate IS: ID, exact version, kind, parameter
//     schema. Immutable once published; changed only through a PR to the
//     library, never from the UI.
//   - GateRef — how a stage USES a definition: exact ID+version, parameters,
//     required/advisory mode. A template pins refs; refs never float.
//   - GateAttempt — what happened when one gate ran on one result version:
//     technical Execution and content Verdict recorded separately.

// GateKind is the way a definition is executed.
type GateKind string

const (
	// GateKindBuiltin is a named Go implementation in this repository.
	GateKindBuiltin GateKind = "builtin"
	// GateKindLLM is a reviewer agent with a fixed input and verdict schema.
	GateKindLLM GateKind = "llm"
	// GateKindHuman is a pause for a human decision bound to the result hash.
	GateKindHuman GateKind = "human"
	// GateKindExternal is a trusted command speaking the JSON gate protocol.
	GateKindExternal GateKind = "external"
	// GateKindDocker runs the same JSON protocol inside a pinned image.
	GateKindDocker GateKind = "docker"
)

// GateKinds lists every kind the contract knows, in presentation order.
var GateKinds = []GateKind{GateKindBuiltin, GateKindLLM, GateKindHuman, GateKindExternal, GateKindDocker}

// Valid reports whether the kind is part of the contract.
func (k GateKind) Valid() bool {
	for _, known := range GateKinds {
		if k == known {
			return true
		}
	}
	return false
}

// GateParamType is the type of one gate parameter.
type GateParamType string

const (
	GateParamString     GateParamType = "string"
	GateParamInt        GateParamType = "int"
	GateParamBool       GateParamType = "bool"
	GateParamStringList GateParamType = "string_list"
	// GateParamEnum is a string restricted to GateParam.Enum.
	GateParamEnum GateParamType = "enum"
)

// GateParam describes one accepted parameter of a gate definition. A
// definition accepts exactly the parameters it declares; anything else is a
// configuration error before the run starts.
type GateParam struct {
	Name        string        `json:"name"`
	Type        GateParamType `json:"type"`
	Required    bool          `json:"required,omitempty"`
	Description string        `json:"description,omitempty"`
	// Enum lists the allowed values for GateParamEnum.
	Enum []string `json:"enum,omitempty"`
	// Min is the inclusive lower bound for GateParamInt.
	Min *int `json:"min,omitempty"`
}

// GateDefinition is one immutable entry of the gate library.
type GateDefinition struct {
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Kind        GateKind `json:"kind"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	// ResultTypes limits the stage result types (md|link|approve) the gate can
	// check. Empty means any.
	ResultTypes []string `json:"result_types,omitempty"`
	// LinkKinds limits the link kinds for result type link. Empty means any.
	LinkKinds []string    `json:"link_kinds,omitempty"`
	Params    []GateParam `json:"params,omitempty"`
}

// Ref returns the exact reference "id@version".
func (d GateDefinition) Ref() string { return fmt.Sprintf("%s@%d", d.ID, d.Version) }

// GateMode says whether a failed verdict blocks the transition.
type GateMode string

const (
	// GateModeRequired blocks the transition until the gate passes (default).
	GateModeRequired GateMode = "required"
	// GateModeAdvisory records a warning and never blocks. Enforcement of the
	// advisory semantics belongs to S06; S02 only carries and validates it.
	GateModeAdvisory GateMode = "advisory"
)

// Valid reports whether the mode is known. The empty mode means required.
func (m GateMode) Valid() bool { return m == "" || m == GateModeRequired || m == GateModeAdvisory }

// Effective returns the mode with the default applied.
func (m GateMode) Effective() GateMode {
	if m == "" {
		return GateModeRequired
	}
	return m
}

// GateRef is a stage's use of a gate definition.
type GateRef struct {
	// Gate is the definition ID.
	Gate string `json:"gate"`
	// Version is the exact definition version. Zero is invalid: references
	// never float to "latest".
	Version int `json:"version"`
	// Name identifies this instance inside the stage (defaults to Gate).
	// It lets one stage use the same definition twice with different params
	// and is the stable key for attempts, retries and evidence.
	Name   string         `json:"name,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	// Mode defaults to required.
	Mode GateMode `json:"mode,omitempty"`
}

// InstanceName returns the stage-unique instance name.
func (r GateRef) InstanceName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Gate
}

// Key returns the exact definition reference "id@version".
func (r GateRef) Key() string { return fmt.Sprintf("%s@%d", r.Gate, r.Version) }

// GateGroup is a set of gates started together. Gates inside one group are
// independent and may run in parallel; the next group starts only after the
// whole previous group finished and every required gate passed (or was
// lawfully overridden).
type GateGroup []GateRef

// GateGroups is the ordered exit chain of one stage: groups run sequentially.
type GateGroups []GateGroup

// Refs returns every reference in execution order.
func (g GateGroups) Refs() []GateRef {
	var refs []GateRef
	for _, group := range g {
		refs = append(refs, group...)
	}
	return refs
}

var gateIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var gateInstanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ValidGateID reports whether id is a well-formed gate definition ID.
func ValidGateID(id string) bool { return gateIDPattern.MatchString(id) }

// ValidateShape checks the reference without consulting any library: ID and
// version format, instance name and mode. The returned error names the
// problem; callers prefix the YAML path.
func (r GateRef) ValidateShape() error {
	if !ValidGateID(r.Gate) {
		return fmt.Errorf("gate %q: ID должен соответствовать ^[a-z][a-z0-9_]{0,63}$", r.Gate)
	}
	if r.Version <= 0 {
		return fmt.Errorf("gate %q: версия обязательна и должна быть положительным целым (пример: %s@1); ссылки на «последнюю» версию не допускаются", r.Gate, r.Gate)
	}
	if r.Name != "" && !gateInstanceNamePattern.MatchString(r.Name) {
		return fmt.Errorf("gate %q: name %q должен соответствовать ^[a-z][a-z0-9_-]{0,63}$", r.Gate, r.Name)
	}
	if !r.Mode.Valid() {
		return fmt.Errorf("gate %q: mode %q недопустим (required|advisory)", r.Gate, r.Mode)
	}
	return nil
}

// ValidateShape checks groups without a library: no empty groups, valid
// references and stage-unique instance names. Error strings carry the
// exit_gate_groups[i][j] path relative to the stage.
func (g GateGroups) ValidateShape() []error {
	var errs []error
	seen := map[string]bool{}
	for i, group := range g {
		if len(group) == 0 {
			errs = append(errs, fmt.Errorf("exit_gate_groups[%d]: группа не может быть пустой", i))
			continue
		}
		for j, ref := range group {
			path := fmt.Sprintf("exit_gate_groups[%d][%d]", i, j)
			if err := ref.ValidateShape(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				continue
			}
			name := ref.InstanceName()
			if seen[name] {
				errs = append(errs, fmt.Errorf("%s: имя гейта %q повторяется на этапе (задайте разные name)", path, name))
			}
			seen[name] = true
		}
	}
	return errs
}

// UnmarshalYAML accepts two spellings of one gate reference:
//
//	"required_sections@1"                      # scalar: id@version, defaults
//	{gate: required_sections, version: 1,      # mapping: explicit
//	 name: ..., mode: advisory, params: {...}}
//
// A scalar without "@version" and a mapping without "version" are rejected
// with a message that shows the fix. Unknown mapping keys are rejected.
func (r *GateRef) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var text string
		if err := node.Decode(&text); err != nil {
			return err
		}
		id, versionText, hasVersion := strings.Cut(strings.TrimSpace(text), "@")
		if !hasVersion {
			return fmt.Errorf("строка %d: ссылка на гейт %q без версии; укажите %s@<версия> (например %s@1)", node.Line, text, text, text)
		}
		var version int
		if _, err := fmt.Sscanf(versionText, "%d", &version); err != nil || fmt.Sprint(version) != versionText || version <= 0 {
			return fmt.Errorf("строка %d: версия в %q должна быть положительным целым", node.Line, text)
		}
		*r = GateRef{Gate: id, Version: version}
		return nil
	case yaml.MappingNode:
		allowed := map[string]bool{"gate": true, "version": true, "name": true, "mode": true, "params": true}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if !allowed[node.Content[i].Value] {
				return fmt.Errorf("строка %d: неизвестное поле %q в ссылке на гейт (допустимы gate, version, name, mode, params)", node.Content[i].Line, node.Content[i].Value)
			}
		}
		type plain struct {
			Gate    string         `yaml:"gate"`
			Version int            `yaml:"version"`
			Name    string         `yaml:"name"`
			Mode    GateMode       `yaml:"mode"`
			Params  map[string]any `yaml:"params"`
		}
		var decoded plain
		if err := node.Decode(&decoded); err != nil {
			return err
		}
		*r = GateRef{Gate: decoded.Gate, Version: decoded.Version, Name: decoded.Name, Mode: decoded.Mode, Params: decoded.Params}
		return nil
	default:
		return fmt.Errorf("строка %d: ссылка на гейт должна быть строкой id@version или mapping", node.Line)
	}
}

// MarshalYAML writes the compact scalar form when the reference carries only
// ID and version, and the explicit mapping otherwise.
func (r GateRef) MarshalYAML() (any, error) {
	if r.Name == "" && r.Mode == "" && len(r.Params) == 0 {
		return r.Key(), nil
	}
	type plain struct {
		Gate    string         `yaml:"gate"`
		Version int            `yaml:"version"`
		Name    string         `yaml:"name,omitempty"`
		Mode    GateMode       `yaml:"mode,omitempty"`
		Params  map[string]any `yaml:"params,omitempty"`
	}
	return plain{Gate: r.Gate, Version: r.Version, Name: r.Name, Mode: r.Mode, Params: r.Params}, nil
}

// GatePin records the exact definition a template or task is bound to.
// Digest is the SHA-256 of the definition's canonical JSON, so evidence can
// prove which definition produced an outcome even if the library later grows.
type GatePin struct {
	Gate    string   `json:"gate"`
	Version int      `json:"version"`
	Kind    GateKind `json:"kind"`
	Digest  string   `json:"digest"`
}

// GateExecution is the technical state of one gate attempt. It says whether
// the gate machinery ran, never what it concluded.
type GateExecution string

const (
	GateExecutionPending   GateExecution = "pending"
	GateExecutionRunning   GateExecution = "running"
	GateExecutionSucceeded GateExecution = "succeeded"
	GateExecutionError     GateExecution = "error"
	GateExecutionTimeout   GateExecution = "timeout"
	GateExecutionCanceled  GateExecution = "canceled"
)

// Terminal reports whether the execution state can no longer change.
func (e GateExecution) Terminal() bool {
	switch e {
	case GateExecutionSucceeded, GateExecutionError, GateExecutionTimeout, GateExecutionCanceled:
		return true
	}
	return false
}

// Technical reports whether the state is a technical failure that may be
// retried on the same result version (error or timeout). A technical failure
// is never an opinion of the checker.
func (e GateExecution) Technical() bool { return e == GateExecutionError || e == GateExecutionTimeout }

// Valid reports whether the execution state is known.
func (e GateExecution) Valid() bool {
	return e == GateExecutionPending || e == GateExecutionRunning || e.Terminal()
}

// GateVerdict is the content conclusion of a gate whose execution succeeded.
type GateVerdict string

const (
	GateVerdictPass          GateVerdict = "pass"
	GateVerdictFail          GateVerdict = "fail"
	GateVerdictNotApplicable GateVerdict = "not_applicable"
)

// Valid reports whether the verdict is known.
func (v GateVerdict) Valid() bool {
	return v == GateVerdictPass || v == GateVerdictFail || v == GateVerdictNotApplicable
}

// GateEvidenceRef points to a sanitized evidence artifact of an attempt.
type GateEvidenceRef struct {
	// Kind is a short label (log, report, diff, ...).
	Kind string `json:"kind"`
	// Path is repository- or run-relative; never an absolute host path.
	Path string `json:"path"`
	// SHA256 is the lowercase hex digest of the stored bytes, when known.
	SHA256 string `json:"sha256,omitempty"`
}

// GateAttempt is the immutable record of one gate invocation on one version
// of a stage result. Execution and Verdict are separate: an error, a timeout
// and a negative verdict must stay distinguishable everywhere (events, API,
// UI). Verdict is set only when Execution is succeeded.
type GateAttempt struct {
	TaskID string `json:"task_id"`
	// Stage is the stage ID; Visit the 1-based visit of that stage.
	Stage string `json:"stage"`
	Visit int    `json:"visit"`
	// ArtifactSHA256 is the hash of the result version this attempt checked.
	// A verdict never carries over to a different hash.
	ArtifactSHA256 string `json:"artifact_sha256"`
	// Gate and GateVersion identify the pinned definition; GateName is the
	// stage-unique instance name of the reference.
	Gate        string `json:"gate"`
	GateVersion int    `json:"gate_version"`
	GateName    string `json:"gate_name"`
	// Mode is the reference mode in force for this attempt.
	Mode GateMode `json:"mode"`
	// AttemptID is unique per attempt; a retry of the same gate on the same
	// hash gets a new AttemptID and does not erase the earlier attempt.
	AttemptID string `json:"attempt_id"`
	// Executor is the kind that ran the attempt.
	Executor GateKind `json:"executor"`
	// Group is the 0-based index of the group in the stage's exit chain.
	Group int `json:"group"`

	Execution GateExecution `json:"execution"`
	Verdict   GateVerdict   `json:"verdict,omitempty"`
	// Reason is a short human-readable explanation, already redacted.
	Reason   string            `json:"reason,omitempty"`
	Evidence []GateEvidenceRef `json:"evidence,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Validate enforces the invariants of the record: identification fields are
// present, the verdict is present exactly when execution succeeded, and a
// finished attempt has a finish time.
func (a GateAttempt) Validate() error {
	var problems []string
	need := func(ok bool, what string) {
		if !ok {
			problems = append(problems, what)
		}
	}
	need(a.TaskID != "", "task_id")
	need(a.Stage != "", "stage")
	need(a.Visit > 0, "visit")
	need(a.ArtifactSHA256 != "", "artifact_sha256")
	need(a.Gate != "" && a.GateVersion > 0, "gate/gate_version")
	need(a.GateName != "", "gate_name")
	need(a.AttemptID != "", "attempt_id")
	need(a.Executor.Valid(), "executor")
	need(a.Mode.Valid(), "mode")
	need(a.Group >= 0, "group")
	need(!a.StartedAt.IsZero(), "started_at")
	if len(problems) > 0 {
		return fmt.Errorf("gate attempt: не заполнены или неверны поля: %s", strings.Join(problems, ", "))
	}
	if !a.Execution.Valid() {
		return fmt.Errorf("gate attempt %s: execution %q неизвестен", a.AttemptID, a.Execution)
	}
	switch {
	case a.Execution == GateExecutionSucceeded && !a.Verdict.Valid():
		return fmt.Errorf("gate attempt %s: execution succeeded требует verdict pass|fail|not_applicable", a.AttemptID)
	case a.Execution != GateExecutionSucceeded && a.Verdict != "":
		return fmt.Errorf("gate attempt %s: verdict допустим только при execution succeeded (получен %s при %s)", a.AttemptID, a.Verdict, a.Execution)
	case a.Execution.Terminal() && a.FinishedAt.IsZero():
		return fmt.Errorf("gate attempt %s: завершённая попытка требует finished_at", a.AttemptID)
	case !a.Execution.Terminal() && !a.FinishedAt.IsZero():
		return fmt.Errorf("gate attempt %s: finished_at недопустим для execution %s", a.AttemptID, a.Execution)
	}
	return nil
}

// Blocking reports whether the attempt, as recorded, prevents the transition.
// It is a pure function of execution, verdict and mode; the controller (S03,
// S06) additionally applies overrides, which never rewrite the attempt.
//
//   - required + succeeded + pass|not_applicable: does not block
//   - required + anything else (fail, error, timeout, canceled, unfinished): blocks
//   - advisory: never blocks
func (a GateAttempt) Blocking() bool {
	if a.Mode.Effective() == GateModeAdvisory {
		return false
	}
	return !(a.Execution == GateExecutionSucceeded && (a.Verdict == GateVerdictPass || a.Verdict == GateVerdictNotApplicable))
}
