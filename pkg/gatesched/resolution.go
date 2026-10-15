package gatesched

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// resolution.go — the aggregated "can the task move on, and why" over one
// result version (S06). It is a pure projection of the recorded attempts and
// of the administrator override: the same function serves the API, the UI and
// MCP, so every view tells the same story. It never rewrites an outcome: a
// gate that failed stays failed even when an administrator let the task pass.

// EventCheckOverridden is the evidence event of an administrator override of
// a failed or erroring stage check. The name is older than the gate contract
// and stays for compatibility with metrics, notifications and the task feed.
const EventCheckOverridden = "check_overridden"

// EventGateRetryRequested records the request to run technically failed gates
// again on the same result version.
const EventGateRetryRequested = "gate_retry_requested"

// AuthoritySystemAdmin marks an override made under the system administrator
// right (cloudidentity.PermissionGateOverride).
const AuthoritySystemAdmin = "system_admin"

// OverriddenGate names the blocking gate attempt an override let pass. The
// attempt is kept exactly as it was recorded.
type OverriddenGate struct {
	GateName  string                 `json:"gate_name"`
	Gate      string                 `json:"gate,omitempty"`
	AttemptID string                 `json:"attempt_id"`
	Execution workflow.GateExecution `json:"execution"`
	Verdict   workflow.GateVerdict   `json:"verdict,omitempty"`
	Mode      workflow.GateMode      `json:"mode,omitempty"`
	Group     int                    `json:"group"`
	Reason    string                 `json:"reason,omitempty"`
}

// Override is the immutable record of an administrator override, bound to one
// result version (ArtifactSHA256) of one stage visit (Scope is the stage
// attempt). A different version of the result is never covered by it.
type Override struct {
	Stage          string           `json:"stage"`
	Scope          string           `json:"scope"`
	Visit          int              `json:"visit,omitempty"`
	ArtifactSHA256 string           `json:"artifact_sha256"`
	Reason         string           `json:"reason"`
	ActorID        string           `json:"actor_id"`
	Authority      string           `json:"authority"`
	ApprovalID     string           `json:"approval_id,omitempty"`
	At             time.Time        `json:"at"`
	CheckStatus    string           `json:"check_status,omitempty"`
	Gates          []OverriddenGate `json:"gates"`
	// Legacy marks an override recorded before S06: it has no result version,
	// no gate list and no authority. It is shown as history only and never
	// extends any right.
	Legacy bool `json:"legacy,omitempty"`
}

// IsLegacy reports an override recorded before the version binding.
func (o Override) IsLegacy() bool { return o.ArtifactSHA256 == "" && len(o.Gates) == 0 }

// DecodeOverride reads an Override from a check_overridden event. Events
// written before S06 carry no version or gate list: they decode with empty
// ArtifactSHA256 and Gates, and cover nothing in Resolve (they are shown as
// legacy overrides by the API).
func DecodeOverride(event evidence.Event) (Override, bool) {
	if event.Type != EventCheckOverridden {
		return Override{}, false
	}
	str := func(key string) string { value, _ := event.Data[key].(string); return value }
	override := Override{
		Stage: event.Stage, Scope: event.AttemptID, ArtifactSHA256: str("artifact_sha256"),
		Reason: str("reason"), ActorID: str("actor_id"), Authority: str("authority"),
		ApprovalID: str("approval_id"), At: event.Timestamp, CheckStatus: str("check_status"),
	}
	if visit, ok := event.Data["visit"].(float64); ok {
		override.Visit = int(visit)
	} else if visit, ok := event.Data["visit"].(int); ok {
		override.Visit = visit
	}
	items, _ := event.Data["gates"].([]any)
	for _, item := range items {
		entry, _ := item.(map[string]any)
		gate := OverriddenGate{}
		gate.GateName, _ = entry["gate_name"].(string)
		gate.Gate, _ = entry["gate"].(string)
		gate.AttemptID, _ = entry["attempt_id"].(string)
		execution, _ := entry["execution"].(string)
		verdict, _ := entry["verdict"].(string)
		mode, _ := entry["mode"].(string)
		gate.Execution, gate.Verdict, gate.Mode = workflow.GateExecution(execution), workflow.GateVerdict(verdict), workflow.GateMode(mode)
		if group, ok := entry["group"].(float64); ok {
			gate.Group = int(group)
		}
		gate.Reason, _ = entry["reason"].(string)
		override.Gates = append(override.Gates, gate)
	}
	override.Legacy = override.IsLegacy()
	return override, true
}

// OverriddenGates is the event representation of the blocking attempts an
// override covers: the attempts exactly as recorded.
func OverriddenGates(blocking []workflow.GateAttempt) []map[string]any {
	out := make([]map[string]any, 0, len(blocking))
	for _, attempt := range blocking {
		item := map[string]any{
			"gate_name": attempt.GateName, "gate": attempt.Gate, "attempt_id": attempt.AttemptID,
			"execution": string(attempt.Execution), "mode": string(attempt.Mode.Effective()), "group": attempt.Group,
		}
		if attempt.Verdict != "" {
			item["verdict"] = string(attempt.Verdict)
		}
		if attempt.Reason != "" {
			item["reason"] = attempt.Reason
		}
		out = append(out, item)
	}
	return out
}

// GateStatus is the effective state of one gate of a version.
type GateStatus struct {
	GateName string            `json:"gate_name"`
	Gate     string            `json:"gate"`
	Version  int               `json:"gate_version"`
	Mode     workflow.GateMode `json:"mode"`
	Group    int               `json:"group"`
	// Attempt is the effective (latest) attempt; nil when the gate did not
	// start (an earlier group blocked).
	Attempt *workflow.GateAttempt `json:"attempt,omitempty"`
	// Attempts is the number of attempts recorded for the gate on this version.
	Attempts int `json:"attempts"`
	// Blocking: the gate prevents the transition (unless overridden).
	Blocking bool `json:"blocking"`
	// Warning: an advisory gate that did not pass. It never blocks.
	Warning bool `json:"warning"`
	// Retryable: the latest attempt is a technical failure (error/timeout)
	// that can be run again on the same version.
	Retryable bool `json:"retryable"`
	// Overridden: the blocking outcome was let pass by an administrator. The
	// attempt itself is unchanged.
	Overridden bool `json:"overridden"`
}

// Resolution states.
const (
	ResolutionPassed             = "passed"
	ResolutionPassedWithWarnings = "passed_with_warnings"
	ResolutionOverridden         = "overridden"
	// ResolutionLegacyOverride: the wave carries a pre-S06 override (no version,
	// no gate list). It explains why the task moved on and is not a right.
	ResolutionLegacyOverride = "legacy_overridden"
	ResolutionBlocked        = "blocked"
	ResolutionWaiting        = "waiting"
	ResolutionError          = "error"
	ResolutionIncomplete     = "incomplete"
)

// Resolution answers whether the stage may transition on one result version.
type Resolution struct {
	Allowed bool   `json:"allowed"`
	State   string `json:"state"`
	// Reasons explain, in words, why the transition is blocked or what it
	// carries (warnings, an override).
	Reasons    []string `json:"reasons"`
	Blocking   []string `json:"blocking"`
	Warnings   []string `json:"warnings"`
	Retryable  []string `json:"retryable"`
	Overridden []string `json:"overridden"`
}

// Resolve folds the attempts of ONE result version (in recording order) into
// the effective state of every configured gate and the aggregated transition
// decision. refs is the configured chain in order; override is the
// administrator override recorded for this very version, if any (an override
// of another version must not be passed in: it is ignored when its hash
// differs from sha).
func Resolve(refs []workflow.GateRef, groupOf func(workflow.GateRef) int, sha string, attempts []workflow.GateAttempt, override *Override) ([]GateStatus, Resolution) {
	byName := map[string][]workflow.GateAttempt{}
	for _, attempt := range attempts {
		byName[attempt.GateName] = append(byName[attempt.GateName], attempt)
	}
	valid := override != nil && override.ArtifactSHA256 != "" && override.ArtifactSHA256 == sha
	legacy := override != nil && override.IsLegacy()
	covered := map[string]bool{}
	if valid {
		for _, gate := range override.Gates {
			covered[gate.AttemptID] = true
		}
	}
	resolution := Resolution{Reasons: []string{}, Blocking: []string{}, Warnings: []string{}, Retryable: []string{}, Overridden: []string{}}
	statuses := make([]GateStatus, 0, len(refs))
	var failed, technical, waiting, notStarted []string
	allCovered := true
	for _, ref := range refs {
		name := ref.InstanceName()
		status := GateStatus{GateName: name, Gate: ref.Gate, Version: ref.Version, Mode: ref.Mode.Effective()}
		if groupOf != nil {
			status.Group = groupOf(ref)
		}
		history := byName[name]
		status.Attempts = len(history)
		if len(history) == 0 {
			// A gate that never started blocks only when it is required.
			status.Blocking = status.Mode == workflow.GateModeRequired
			if status.Blocking {
				notStarted = append(notStarted, name)
			}
			statuses = append(statuses, status)
			continue
		}
		latest := history[len(history)-1]
		status.Attempt = &latest
		status.Group = latest.Group
		status.Mode = latest.Mode.Effective()
		status.Blocking = latest.Blocking()
		// Only a blocking gate is retried: the advisory one never opens a
		// decision, so offering a retry for it would end in a conflict.
		status.Retryable = latest.Execution.Technical() && status.Blocking
		if status.Mode == workflow.GateModeAdvisory && !passed(latest) {
			status.Warning = true
			resolution.Warnings = append(resolution.Warnings, name)
		}
		if status.Retryable {
			resolution.Retryable = append(resolution.Retryable, name)
		}
		if status.Blocking {
			resolution.Blocking = append(resolution.Blocking, name)
			switch {
			case latest.Execution == workflow.GateExecutionSucceeded:
				failed = append(failed, name)
			case latest.Execution == workflow.GateExecutionPending || latest.Execution == workflow.GateExecutionRunning:
				waiting = append(waiting, name)
			default:
				technical = append(technical, name)
			}
			if covered[latest.AttemptID] {
				status.Overridden = true
				resolution.Overridden = append(resolution.Overridden, name)
			} else {
				allCovered = false
			}
		}
		statuses = append(statuses, status)
	}
	resolution.Blocking = append(resolution.Blocking, notStarted...)
	sort.Strings(resolution.Warnings)
	switch {
	case len(resolution.Blocking) == 0:
		resolution.Allowed = true
		resolution.State = ResolutionPassed
		if len(resolution.Warnings) > 0 {
			resolution.State = ResolutionPassedWithWarnings
			resolution.Reasons = append(resolution.Reasons, "рекомендательные гейты не прошли, переход не заблокирован: "+strings.Join(resolution.Warnings, ", "))
		}
	case legacy:
		// History only: the task already moved on under an override recorded
		// before S06 (no version, no gate list). The wave must not look blocked
		// for a task that really passed; no right is derived from it.
		resolution.Allowed = false
		resolution.State = ResolutionLegacyOverride
		resolution.Reasons = append(resolution.Reasons, fmt.Sprintf(
			"legacy: переход был разрешён обходом без привязки к версии результата (%s): %s",
			override.ActorID, strings.TrimSpace(override.Reason)))
	case valid && allCovered:
		// Gates that never started are skipped by the override as well: it was
		// made knowing which groups had not run.
		resolution.Allowed = true
		resolution.State = ResolutionOverridden
		resolution.Reasons = append(resolution.Reasons, fmt.Sprintf(
			"переход разрешён администратором %s несмотря на %s; исход гейтов не изменён: %s",
			override.ActorID, strings.Join(resolution.Overridden, ", "), strings.TrimSpace(override.Reason)))
		if len(resolution.Warnings) > 0 {
			resolution.Reasons = append(resolution.Reasons, "рекомендательные гейты не прошли: "+strings.Join(resolution.Warnings, ", "))
		}
	default:
		switch {
		case len(failed) > 0:
			resolution.State = ResolutionBlocked
			resolution.Reasons = append(resolution.Reasons, "обязательные гейты вернули fail: "+strings.Join(failed, ", "))
		case len(technical) > 0:
			resolution.State = ResolutionError
			resolution.Reasons = append(resolution.Reasons, "обязательные гейты завершились технической ошибкой (error/timeout), их можно повторить на той же версии: "+strings.Join(technical, ", "))
		case len(waiting) > 0:
			resolution.State = ResolutionWaiting
			resolution.Reasons = append(resolution.Reasons, "ждём завершения или решения: "+strings.Join(waiting, ", "))
		default:
			resolution.State = ResolutionIncomplete
		}
		if len(notStarted) > 0 {
			resolution.Reasons = append(resolution.Reasons, "ещё не запускались: "+strings.Join(notStarted, ", "))
		}
		if len(resolution.Warnings) > 0 {
			resolution.Reasons = append(resolution.Reasons, "рекомендательные гейты не прошли: "+strings.Join(resolution.Warnings, ", "))
		}
		if override != nil && !valid {
			resolution.Reasons = append(resolution.Reasons, "обход относится к другой версии результата и не действует на эту")
		} else if valid && !allCovered {
			resolution.Reasons = append(resolution.Reasons, "обход не покрывает текущие попытки блокирующих гейтов")
		}
	}
	return statuses, resolution
}
