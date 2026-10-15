// Package llmgate is the contract of the LLM gate: a reviewer agent is called
// with a fixed input and its answer must match a declared verdict schema.
//
// The package knows nothing about stages, runtimes or the grill machinery: the
// caller supplies a function that performs one agent call and returns the
// agent's report. Run applies the contract around it:
//
//   - the report must match the configured schema; an answer outside the
//     schema is a technical error, never a pass and never a fail;
//   - a refusal of the runtime (the call returned an error) is a technical
//     error as well;
//   - technical failures are retried up to the configured number of attempts,
//     and every attempt stays in the trace (nothing is hidden by a retry);
//   - a valid verdict, positive or negative, ends the gate at once: an opinion
//     of the reviewer is never retried.
//
// mode grill (question rounds with a human) is one way of running the gate in
// the pipeline and is deliberately not part of this contract.
package llmgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
)

// Schema names a verdict schema of the reviewer's answer.
type Schema string

const (
	// SchemaMarker is the line marker contract: a line outside code blocks and
	// quotes such as `Verdict: PASSED | FAILED` (pkg/stagecheck.ParseVerdict).
	SchemaMarker Schema = "verdict_marker"
	// SchemaJSON is the structured contract: one JSON object
	//   {"verdict":"pass|fail","reason":"...","findings":[{"severity":"...","message":"..."}]}
	// either as the whole report or as the only fenced json block of it.
	SchemaJSON Schema = "json"
)

// Schemas lists the accepted schema names, default first.
var Schemas = []string{string(SchemaMarker), string(SchemaJSON)}

// Attempt limits.
const (
	DefaultMaxAttempts = 1
	MaxAttempts        = 5
)

// Limits of the structured answer. They bound what a model can put into the
// durable record.
const (
	maxReasonBytes   = 4 << 10
	maxFindings      = 100
	maxFindingBytes  = 2 << 10
	maxReportBytes   = 1 << 20
	maxReasonSummary = 600
)

// Severities accepted in findings of the json schema.
var severities = map[string]bool{"blocker": true, "major": true, "minor": true, "info": true}

// Finding is one remark of the reviewer.
type Finding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Verdict is a valid answer of the reviewer.
type Verdict struct {
	Pass     bool
	Reason   string
	Findings []Finding
}

// ErrSchema marks an answer that does not match the configured schema.
var ErrSchema = errors.New("ответ проверяющего не соответствует схеме вердикта")

// Normalize returns the schema to use for an empty name.
func Normalize(schema Schema) Schema {
	if schema == "" {
		return SchemaMarker
	}
	return schema
}

// Valid reports whether the schema is known (empty means the default).
func (s Schema) Valid() bool {
	switch Normalize(s) {
	case SchemaMarker, SchemaJSON:
		return true
	}
	return false
}

// Parse reads the verdict of report under the schema. Every failure wraps
// ErrSchema.
func Parse(schema Schema, report string) (Verdict, error) {
	if len(report) > maxReportBytes {
		return Verdict{}, fmt.Errorf("%w: отчёт больше %d байт", ErrSchema, maxReportBytes)
	}
	switch Normalize(schema) {
	case SchemaMarker:
		value, ok := stagecheck.ParseVerdict(report)
		if !ok {
			return Verdict{}, fmt.Errorf("%w: в отчёте не найден вердикт PASSED|FAILED", ErrSchema)
		}
		return Verdict{Pass: value == "PASSED"}, nil
	case SchemaJSON:
		return parseJSON(report)
	default:
		return Verdict{}, fmt.Errorf("%w: неизвестная схема %q", ErrSchema, schema)
	}
}

type jsonAnswer struct {
	Verdict  *string   `json:"verdict"`
	Reason   string    `json:"reason"`
	Findings []Finding `json:"findings"`
}

var fenceStart = regexp.MustCompile("(?m)^[ \t]*```[ \t]*json[ \t]*$")

func parseJSON(report string) (Verdict, error) {
	body, err := jsonBody(report)
	if err != nil {
		return Verdict{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var answer jsonAnswer
	if err := decoder.Decode(&answer); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	if decoder.More() {
		return Verdict{}, fmt.Errorf("%w: после объекта вердикта есть лишние данные", ErrSchema)
	}
	if answer.Verdict == nil {
		return Verdict{}, fmt.Errorf("%w: поле verdict обязательно", ErrSchema)
	}
	var pass bool
	switch strings.ToLower(strings.TrimSpace(*answer.Verdict)) {
	case "pass":
		pass = true
	case "fail":
	default:
		return Verdict{}, fmt.Errorf("%w: verdict %q, ожидается pass или fail", ErrSchema, *answer.Verdict)
	}
	if len(answer.Reason) > maxReasonBytes {
		return Verdict{}, fmt.Errorf("%w: reason больше %d байт", ErrSchema, maxReasonBytes)
	}
	if len(answer.Findings) > maxFindings {
		return Verdict{}, fmt.Errorf("%w: больше %d замечаний", ErrSchema, maxFindings)
	}
	blocker := false
	for i, finding := range answer.Findings {
		if !severities[finding.Severity] {
			return Verdict{}, fmt.Errorf("%w: findings[%d].severity %q, ожидается blocker, major, minor или info", ErrSchema, i, finding.Severity)
		}
		if strings.TrimSpace(finding.Message) == "" || len(finding.Message) > maxFindingBytes {
			return Verdict{}, fmt.Errorf("%w: findings[%d].message пуст или больше %d байт", ErrSchema, i, maxFindingBytes)
		}
		blocker = blocker || finding.Severity == "blocker"
	}
	reason := strings.TrimSpace(answer.Reason)
	if !pass && reason == "" && len(answer.Findings) == 0 {
		return Verdict{}, fmt.Errorf("%w: отрицательный вердикт требует reason или findings", ErrSchema)
	}
	if pass && blocker {
		return Verdict{}, fmt.Errorf("%w: pass противоречит замечанию severity=blocker", ErrSchema)
	}
	return Verdict{Pass: pass, Reason: reason, Findings: answer.Findings}, nil
}

// jsonBody returns the JSON object of the report: the whole report when it is
// an object, otherwise the only fenced ```json block. Prose around a block is
// allowed; two blocks are ambiguous and rejected.
func jsonBody(report string) ([]byte, error) {
	trimmed := strings.TrimSpace(report)
	if strings.HasPrefix(trimmed, "{") {
		return []byte(trimmed), nil
	}
	starts := fenceStart.FindAllStringIndex(report, -1)
	switch len(starts) {
	case 0:
		return nil, fmt.Errorf("%w: JSON-объект вердикта не найден", ErrSchema)
	case 1:
	default:
		return nil, fmt.Errorf("%w: в отчёте несколько блоков json, вердикт неоднозначен", ErrSchema)
	}
	rest := report[starts[0][1]:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return nil, fmt.Errorf("%w: блок json не закрыт", ErrSchema)
	}
	return []byte(strings.TrimSpace(rest[:end])), nil
}

// Call performs one agent invocation (1-based attempt number) and returns the
// agent's report. A returned error is a refusal of the runtime.
type Call func(ctx context.Context, attempt int) (string, error)

// Spec configures Run.
type Spec struct {
	Schema      Schema
	MaxAttempts int
}

// AttemptClass says how one agent call ended.
type AttemptClass string

const (
	// ClassVerdict: the answer matched the schema.
	ClassVerdict AttemptClass = "verdict"
	// ClassSchema: the answer was outside the schema.
	ClassSchema AttemptClass = "schema"
	// ClassRuntime: the runtime refused or failed.
	ClassRuntime AttemptClass = "runtime"
	// ClassCanceled: the context ended.
	ClassCanceled AttemptClass = "canceled"
)

// AttemptTrace is the record of one agent call.
type AttemptTrace struct {
	N      int          `json:"n"`
	Class  AttemptClass `json:"class"`
	Detail string       `json:"detail,omitempty"`
}

// Status is the aggregate outcome of Run.
type Status string

const (
	StatusPass     Status = "pass"
	StatusFail     Status = "fail"
	StatusError    Status = "error"
	StatusCanceled Status = "canceled"
)

// Result is the outcome of Run.
type Result struct {
	Status  Status
	Verdict Verdict
	// Reason is a short explanation: the reviewer's reason for a verdict, the
	// trace summary for an error.
	Reason string
	Trace  []AttemptTrace
}

// Run performs the agent call until a valid verdict arrives or the attempts
// are spent. It never turns a technical failure into a verdict.
func Run(ctx context.Context, spec Spec, call Call) Result {
	attempts := spec.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	if attempts > MaxAttempts {
		attempts = MaxAttempts
	}
	var result Result
	for n := 1; n <= attempts; n++ {
		if err := ctx.Err(); err != nil {
			result.Trace = append(result.Trace, AttemptTrace{N: n, Class: ClassCanceled, Detail: err.Error()})
			result.Status, result.Reason = StatusCanceled, summarize(result.Trace, attempts)
			return result
		}
		report, err := call(ctx, n)
		if err != nil {
			if ctx.Err() != nil {
				result.Trace = append(result.Trace, AttemptTrace{N: n, Class: ClassCanceled, Detail: err.Error()})
				result.Status, result.Reason = StatusCanceled, summarize(result.Trace, attempts)
				return result
			}
			result.Trace = append(result.Trace, AttemptTrace{N: n, Class: ClassRuntime, Detail: err.Error()})
			continue
		}
		value, err := Parse(spec.Schema, report)
		if err != nil {
			result.Trace = append(result.Trace, AttemptTrace{N: n, Class: ClassSchema, Detail: err.Error()})
			continue
		}
		result.Trace = append(result.Trace, AttemptTrace{N: n, Class: ClassVerdict})
		result.Verdict = value
		result.Reason = verdictReason(value)
		if value.Pass {
			result.Status = StatusPass
		} else {
			result.Status = StatusFail
		}
		return result
	}
	result.Status, result.Reason = StatusError, summarize(result.Trace, attempts)
	return result
}

func verdictReason(value Verdict) string {
	reason := value.Reason
	if reason == "" && len(value.Findings) > 0 {
		parts := make([]string, 0, len(value.Findings))
		for _, finding := range value.Findings {
			parts = append(parts, finding.Severity+": "+finding.Message)
		}
		reason = strings.Join(parts, "; ")
	}
	return clip(reason)
}

func summarize(trace []AttemptTrace, attempts int) string {
	parts := make([]string, 0, len(trace))
	for _, item := range trace {
		part := fmt.Sprintf("попытка %d/%d: %s", item.N, attempts, classLabel(item.Class))
		if item.Detail != "" {
			part += " (" + item.Detail + ")"
		}
		parts = append(parts, part)
	}
	return clip(strings.Join(parts, "; "))
}

func classLabel(class AttemptClass) string {
	switch class {
	case ClassSchema:
		return "ответ вне схемы"
	case ClassRuntime:
		return "отказ рантайма"
	case ClassCanceled:
		return "отменена"
	}
	return string(class)
}

func clip(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxReasonSummary {
		return text
	}
	return strings.ToValidUTF8(text[:maxReasonSummary], "") + "…"
}

// MarkerStatusOf maps a marker-schema verdict to the legacy report verdict
// name used by pkg/stagecheck.
func MarkerStatusOf(pass bool) string {
	if pass {
		return "PASSED"
	}
	return "FAILED"
}
