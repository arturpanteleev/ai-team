package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ValidateWorkerEventType is the API boundary allowlist. Unknown event types
// are rejected even if they form a valid hash chain, because replay ignores
// worker-supplied extension events by design for historical compatibility.
func ValidateWorkerEventType(eventType string) error {
	switch eventType {
	case "run_started", "run_resumed", "run_paused", "run_finished", "run_canceled",
		"attempt_started", "attempt_finished", "attempt_abandoned", "attempts_invalidated",
		"approval_requested", "approval_decided", "approval_reused", "transition_selected",
		"delivery_deferred", "delivery_plan_approved", "deferred_gates_ratified",
		"test_mutations", "resume_blocked":
		return nil
	default:
		return fmt.Errorf("event type %q is not allowed through worker API", eventType)
	}
}

// ValidateEventAppend applies the normal lifecycle replay rules to the
// candidate append before it becomes durable. Existing events must come from
// a verified EventLog.Read result. The attempt manifest source is supplied by
// the controller so workers cannot validate a forged attempt_finished event
// against a worker-visible manifest mirror.
func ValidateEventAppend(events []Event, runID, runDir string, candidate Event, expectedSequence uint64, expectedPreviousSHA256 string, manifests AttemptManifestSource) (Event, bool, error) {
	return validateEventAppend(events, runID, runDir, candidate, expectedSequence, expectedPreviousSHA256, manifests, false)
}

// ValidateControllerEventAppend is the controller-only lifecycle append path.
// It permits the narrowly typed description_missing event while the ordinary
// worker event boundary continues to reject that controller-owned event.
func ValidateControllerEventAppend(events []Event, runID, runDir string, candidate Event, expectedSequence uint64, expectedPreviousSHA256 string, manifests AttemptManifestSource) (Event, bool, error) {
	return validateEventAppend(events, runID, runDir, candidate, expectedSequence, expectedPreviousSHA256, manifests, true)
}

// ValidateControllerHumanAttemptAppend validates an attempt event constructed
// by the trusted controller from a resolved human input approval.
func ValidateControllerHumanAttemptAppend(events []Event, runID, runDir string, candidate Event, expectedSequence uint64, expectedPreviousSHA256 string, manifests AttemptManifestSource) (Event, bool, error) {
	if (candidate.Type != "attempt_started" && candidate.Type != "attempt_finished") || candidate.Data["executor"] != "human" {
		return Event{}, false, errors.New("controller human attempt append requires a human attempt event")
	}
	return validateEventAppend(events, runID, runDir, candidate, expectedSequence, expectedPreviousSHA256, manifests, true)
}

// ValidateControllerStageSkipAppend validates a warning that the trusted
// controller derives from a resolved human input approval. Worker API clients
// cannot append stage_skipped directly; the controller calls this only after
// authorizing the related human attempt.
func ValidateControllerStageSkipAppend(events []Event, runID, runDir string, candidate Event, expectedSequence uint64, expectedPreviousSHA256 string, manifests AttemptManifestSource) (Event, bool, error) {
	if candidate.Type != "stage_skipped" {
		return Event{}, false, errors.New("controller stage skip append requires stage_skipped")
	}
	return validateEventAppend(events, runID, runDir, candidate, expectedSequence, expectedPreviousSHA256, manifests, true)
}

func validateEventAppend(events []Event, runID, runDir string, candidate Event, expectedSequence uint64, expectedPreviousSHA256 string, manifests AttemptManifestSource, controllerOwned bool) (Event, bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return Event{}, false, err
	}
	if controllerOwned && candidate.Type == "description_missing" {
		// Only this known controller-owned type is admitted by this path.
		if candidate.Stage == "" || candidate.AttemptID == "" || len(candidate.Data) != 2 || candidate.Data["field"] != "description" {
			return Event{}, false, errors.New("description_missing must bind one stage attempt and description field")
		}
		if _, ok := candidate.Data["approval_id"].(string); !ok || candidate.Data["approval_id"] == "" {
			return Event{}, false, errors.New("description_missing must bind its input approval")
		}
	} else if !controllerOwned || candidate.Type != "stage_skipped" {
		if err := ValidateWorkerEventType(candidate.Type); err != nil {
			return Event{}, false, err
		}
	}
	// Raw worker appends cannot choose a human executor or claim skip authority.
	// The trusted controller uses the controller-specific helpers above only
	// after deriving each event from a resolved approval.
	if candidate.Type == "attempt_started" {
		executor, _ := candidate.Data["executor"].(string)
		_, hasAction := candidate.Data["stage_action"]
		_, hasVersion := candidate.Data["stage_skip_version"]
		if executor == "human" && !controllerOwned {
			return Event{}, false, errors.New("worker event append cannot claim human executor")
		}
		if hasAction || hasVersion {
			stageAction, actionOK := candidate.Data["stage_action"].(string)
			version, versionErr := optionalEventInt(candidate.Data, "stage_skip_version")
			if !controllerOwned || executor != "human" || !actionOK || stageAction != "skip" || versionErr != nil || version != StageSkipProtocolVersion {
				return Event{}, false, errors.New("worker cannot claim an unauthorized stage skip")
			}
		}
		if executor != "human" {
			for _, field := range []string{"actor_id", "actor_role", "human_input_approval_id"} {
				if _, claimed := candidate.Data[field]; claimed {
					return Event{}, false, errors.New("worker cannot claim human stage identity")
				}
			}
		}
	}
	if candidate.Type == "attempt_finished" {
		executor, _ := candidate.Data["executor"].(string)
		status, _ := candidate.Data["status"].(string)
		outcome, _ := candidate.Data["outcome"].(string)
		if executor == "human" && !controllerOwned {
			return Event{}, false, errors.New("worker event append cannot claim human executor")
		}
		if executor != "human" {
			if _, claimed := candidate.Data["stage_skip_reason"]; claimed {
				return Event{}, false, errors.New("worker cannot claim controller-owned stage skip reason")
			}
			if status == string(workflow.OutcomeSkipped) || outcome == string(workflow.OutcomeSkipped) {
				return Event{}, false, errors.New("worker cannot claim a skipped agent outcome")
			}
		}
	}
	if uint64(len(events)) == expectedSequence+1 && len(events) > 0 &&
		candidate.Sequence == 0 && candidate.RunID == "" && candidate.SHA256 == "" && candidate.PreviousSHA256 == "" &&
		sameRetryEvent(events[len(events)-1], candidate, runID, expectedSequence+1, expectedPreviousSHA256) {
		return events[len(events)-1], true, nil
	}
	if uint64(len(events)) != expectedSequence {
		return Event{}, false, errors.New("event log changed outside current store")
	}
	lastHash := chainGenesis(runID)
	if len(events) > 0 {
		lastHash = events[len(events)-1].SHA256
	}
	if lastHash != expectedPreviousSHA256 {
		return Event{}, false, errors.New("event log changed outside current store")
	}
	candidate.SchemaVersion = SchemaVersion
	candidate.Sequence = expectedSequence + 1
	candidate.RunID = runID
	if candidate.Timestamp.IsZero() {
		candidate.Timestamp = time.Now().UTC()
	}
	candidate.PreviousSHA256 = expectedPreviousSHA256
	if candidate.Data != nil {
		encoded, err := json.Marshal(candidate.Data)
		if err != nil {
			return Event{}, false, err
		}
		var normalized map[string]any
		if err := json.Unmarshal(encoded, &normalized); err != nil {
			return Event{}, false, err
		}
		candidate.Data = normalized
	}
	digest, err := eventDigest(candidate)
	if err != nil {
		return Event{}, false, err
	}
	candidate.SHA256 = digest
	proposal := append(append([]Event(nil), events...), candidate)
	appendReplay, err := replayEventsForAppend(proposal, runID, runDir, manifests)
	if err != nil {
		return Event{}, false, fmt.Errorf("event append fails lifecycle replay: %w", err)
	}
	finishesExplicitSkip := false
	if candidate.Type == "attempt_finished" {
		for _, attempt := range appendReplay.Attempts {
			if attempt.AttemptID == candidate.AttemptID && attempt.State.Outcome == workflow.OutcomeSkipped &&
				(attempt.StageAction == "skip" || attempt.Executor == "human") {
				finishesExplicitSkip = true
				break
			}
		}
	}
	if !finishesExplicitSkip {
		if _, err := replayEventsWithAttemptManifestSource(proposal, runID, runDir, manifests); err != nil {
			return Event{}, false, fmt.Errorf("event append fails strict lifecycle replay: %w", err)
		}
	}
	if candidate.Type == "run_finished" {
		status, _ := candidate.Data["status"].(string)
		if status == "completed" || status == "completed_with_warnings" {
			finished := 0
			for _, existing := range proposal {
				if existing.Type == "attempt_finished" {
					finished++
				}
			}
			if finished == 0 {
				return Event{}, false, errors.New("successful terminal event requires at least one completed attempt")
			}
		}
	}
	return candidate, false, nil
}

func validEventRelativePath(value string) bool {
	clean := filepath.Clean(filepath.FromSlash(value))
	return value != "" && value != "." && !filepath.IsAbs(value) && clean == filepath.FromSlash(value) &&
		filepath.IsLocal(clean)
}

// ValidDeliveryStatePath checks the pipeline's absolute path claim against the
// run target and the exact prepared-delivery directory.
func ValidDeliveryStatePath(runDir, value string) bool {
	if runDir == "" || value == "" || !filepath.IsAbs(runDir) || !filepath.IsAbs(value) {
		return false
	}
	targetDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(runDir))))
	runID := filepath.Base(filepath.Clean(runDir))
	if ValidDeliveryStatePathForTargetAndRun(targetDir, runID, value) {
		return true
	}
	// The local run pipeline canonicalizes its target before it creates a Git
	// candidate, so on systems such as macOS the event path can use the real
	// target spelling while runDir came from a symlink spelling. Resolve only
	// the run's known target root; never follow or open the event-supplied path.
	canonicalTarget, err := filepath.EvalSymlinks(targetDir)
	return err == nil && canonicalTarget != targetDir &&
		ValidDeliveryStatePathForTargetAndRun(canonicalTarget, runID, value)
}

// ValidDeliveryStatePathForTarget checks the ordinary target delivery
// directory without opening or resolving the path.
func ValidDeliveryStatePathForTarget(targetDir, value string) bool {
	return validDeliveryStatePathForTarget(targetDir, "", value)
}

// ValidDeliveryStatePathForTargetAndRun also accepts the exact candidate
// worktree delivery directory assigned to runID. It validates path identity
// lexically and never opens or resolves the event-supplied path.
func ValidDeliveryStatePathForTargetAndRun(targetDir, runID, value string) bool {
	if err := ValidateRunID(runID); err != nil {
		return false
	}
	return validDeliveryStatePathForTarget(targetDir, runID, value)
}

func validDeliveryStatePathForTarget(targetDir, runID, value string) bool {
	if targetDir == "" || value == "" || !filepath.IsAbs(targetDir) || !filepath.IsAbs(value) ||
		filepath.Clean(targetDir) != targetDir || filepath.Clean(value) != value {
		return false
	}
	withinDeliveryRoot := func(root string) bool {
		relative, err := filepath.Rel(root, value)
		return err == nil && filepath.IsLocal(relative) && relative != "." && filepath.Dir(relative) == "." && filepath.Ext(relative) == ".json"
	}
	if withinDeliveryRoot(filepath.Join(targetDir, ".ai-team", "delivery")) {
		return true
	}
	return runID != "" && withinDeliveryRoot(filepath.Join(targetDir, ".ai-team", "worktrees", runID, ".ai-team", "delivery"))
}
