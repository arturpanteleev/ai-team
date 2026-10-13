package web

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// errSelfApprovalUnverifiable is returned when the self-approval policy is
// enabled but the stage authors cannot be determined. The check fails closed.
var errSelfApprovalUnverifiable = errors.New("cloud RBAC: не удалось определить авторов этапа для проверки самоподтверждения")

// selfApprovalViolation enforces the pinned template's forbid_self_approval
// setting (B-82) for a stage confirmation. It returns nil when the policy is
// disabled, the run has no pinned template, or actorID did not take or submit
// the stage in the visit being confirmed.
func (s *Server) selfApprovalViolation(runID string, pending approval.PendingApproval, actorID string) error {
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil {
		return errSelfApprovalUnverifiable
	}
	if !found || cfg == nil || !cfg.ForbidSelfApproval {
		return nil
	}
	authors, err := s.stageWorkAuthors(runID, pending)
	if err != nil {
		return errSelfApprovalUnverifiable
	}
	return cloudidentity.CheckSelfApproval(true, actorID, authors)
}

// errResumeAwaitingDecision means the run still waits for a human decision;
// resume is a conflict, not a storage failure.
var errResumeAwaitingDecision = errors.New("run всё ещё ожидает human decision")

// errResumeRunActive means the run is executing right now: Resume would be
// refused, so nothing is launched and no authorship is recorded.
var errResumeRunActive = errors.New("run уже исполняется")

// activeRunReporter is implemented by run controllers that know which runs
// they are executing (control.Controller).
type activeRunReporter interface {
	RunActive(runID string) bool
}

// stageAgentRequestedEvent records the actor who launched a stage agent, so
// the launch counts as authorship for forbid_self_approval.
const stageAgentRequestedEvent = "stage_agent_requested"

// stageAuthorEventTypes are run events whose actor worked on the stage.
var stageAuthorEventTypes = map[string]bool{stageAgentRequestedEvent: true, "executor_changed": true}

// stageWorkAuthors returns every human who worked on the stage in the visit
// confirmed by pending. The visit starts after the previous resolved
// confirmation of the same stage (or at the run start). Authors are:
//   - actors of decisions on human input, grill (answer/new_version/done) and
//     check-override approvals of the stage in the visit;
//   - the owner who took a human input of the visit;
//   - actors who launched the stage agent (run-agent or resume of an agent
//     stage) or switched its executor (lifecycle override or event);
//   - actors who appended a revision of the stage result or of an output of
//     any stage attempt in the visit (agent output edits included).
//
// Any read error is returned so the caller fails closed.
func (s *Server) stageWorkAuthors(runID string, pending approval.PendingApproval) ([]string, error) {
	stageID := pending.FromStage
	if s.controller == nil || stageID == "" {
		return nil, fmt.Errorf("stage confirmation context unavailable")
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return nil, err
	}
	var visitStart time.Time
	for _, value := range values {
		if value.ID == pending.ID || value.FromStage != stageID || !isStageConfirmation(value) ||
			value.Status != approval.StatusResolved || !value.CreatedAt.Before(pending.CreatedAt) {
			continue
		}
		resolvedAt := value.ResolvedAt
		if resolvedAt.IsZero() {
			resolvedAt = value.CreatedAt
		}
		if resolvedAt.After(visitStart) {
			visitStart = resolvedAt
		}
	}
	inVisit := func(at time.Time) bool { return visitStart.IsZero() || at.After(visitStart) }

	authors := []string{}
	inputIDs := map[string]bool{}
	attempts := map[string]bool{}
	if pending.AttemptID != "" {
		attempts[pending.AttemptID] = true
	}
	for _, value := range values {
		if value.ID == pending.ID || value.FromStage != stageID || !inVisit(value.CreatedAt) {
			continue
		}
		switch value.Kind {
		case approval.KindInput, approval.KindGrillQuestions, approval.KindCheckDecision:
		default:
			continue
		}
		if value.Kind == approval.KindInput {
			inputIDs[value.ID] = true
		}
		if value.AttemptID != "" {
			attempts[value.AttemptID] = true
		}
		for _, decision := range value.Decisions {
			authors = append(authors, decision.ActorID)
		}
	}

	// The lifecycle checkpoint is the durable record of executor switches made
	// through either executor route, even when no web event was projected.
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		return nil, err
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		return nil, err
	}
	if override, ok := state.ExecutorOverrides[stageID]; ok && inVisit(override.ChangedAt) {
		if override.ActorID == "" {
			return nil, fmt.Errorf("executor override of stage %s has no actor", stageID)
		}
		authors = append(authors, override.ActorID)
	}

	events, err := s.allRunEvents(runID)
	if err != nil {
		return nil, err
	}
	owners, err := s.store.GetStageOwners(runID)
	if err != nil {
		return nil, err
	}
	if owner, ok := owners[stageID]; ok && inputIDs[owner.ApprovalID] {
		authors = append(authors, owner.ActorID)
	}
	for _, event := range events {
		if stageAuthorEventTypes[event.Type] && event.Stage == stageID && inVisit(event.Timestamp) {
			if event.Actor == "" {
				return nil, fmt.Errorf("stage event %s has no actor", event.Type)
			}
			authors = append(authors, event.Actor)
		}
	}

	paths := []string{humanStageResultPath(stageID, "md"), humanStageResultPath(stageID, "link"), humanStageResultPath(stageID, "approve")}
	for attemptID := range attempts {
		if !safeIdentity(attemptID) {
			return nil, fmt.Errorf("invalid attempt id %q", attemptID)
		}
		_, manifest, readErr := evidence.ReadAttemptManifest(nil, filepath.Join(s.runRoot, runID), runID, attemptID)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		for _, output := range manifest.Outputs {
			paths = append(paths, output.EvidencePath)
		}
	}
	for _, path := range paths {
		revisions, listErr := s.humanArtifacts.List(runID, path)
		if listErr != nil {
			return nil, listErr
		}
		for _, revision := range revisions {
			if inVisit(revision.CreatedAt) || inputIDs[revision.ApprovalID] {
				authors = append(authors, revision.ActorID)
			}
		}
	}
	return authors, nil
}

// isStageConfirmation reports whether pending is a generated stage transition
// approval (as opposed to human input, check decisions, grill, questions or the
// delivery plan).
func isStageConfirmation(pending approval.PendingApproval) bool {
	return (pending.Kind == "" || pending.Kind == approval.KindApprove) &&
		pending.Trigger != "delivery_plan" && pending.FromStage != ""
}

// recordResumeAgentLaunch records the actor who is about to resume a run
// whose next step launches an agent stage of the pinned task template, so the
// launch counts as stage authorship for forbid_self_approval. It covers every
// phase Resume accepts: a resumable or interrupted running checkpoint resumes
// at NextStage; a waiting checkpoint whose approval is resolved continues at
// NextStage or at the target of the resolved action, and both are recorded.
//
// It fails closed: any error reading the lifecycle checkpoint, the pinned
// template or the approval is returned, and the caller must not resume. A run
// without a lifecycle checkpoint or a pinned template has nothing to record
// (Resume itself refuses a run without a checkpoint).
func (s *Server) recordResumeAgentLaunch(runID, actorID string) error {
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		return fmt.Errorf("lifecycle store: %w", err)
	}
	state, err := stateStore.Load(runID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lifecycle state: %w", err)
	}
	stageIDs := []string{state.NextStage}
	switch state.Phase {
	case lifecycle.PhaseResumable, lifecycle.PhaseRunning:
	case lifecycle.PhaseWaiting:
		if s.controller == nil {
			return errors.New("approval state unavailable")
		}
		values, err := s.controller.Approvals(runID)
		if err != nil {
			return fmt.Errorf("approval state: %w", err)
		}
		var pending *approval.PendingApproval
		for index := range values {
			if values[index].ID == state.PendingApprovalID {
				pending = &values[index]
				break
			}
		}
		if pending == nil {
			return fmt.Errorf("waiting approval %q not found", state.PendingApprovalID)
		}
		if pending.Status != approval.StatusResolved {
			// Nothing will launch: refuse here so Resume is never called for a
			// run that still waits for a decision.
			return errResumeAwaitingDecision
		}
		stageIDs = append(stageIDs, pending.Targets[pending.ResolvedAction])
	default:
		return nil
	}
	// A running checkpoint is also what an active run looks like. Resume of an
	// active run is refused, so the launch must not be recorded: the actor did
	// not start the agent and must not become an author of its stage.
	if reporter, ok := s.controller.(activeRunReporter); ok && reporter.RunActive(runID) {
		return errResumeRunActive
	}
	cfg, found, err := s.pinnedTaskConfig(runID)
	if err != nil {
		return fmt.Errorf("pinned template: %w", err)
	}
	if !found {
		return nil
	}
	recorded := map[string]bool{}
	for _, stageID := range stageIDs {
		if stageID == "" || recorded[stageID] {
			continue
		}
		recorded[stageID] = true
		for _, stage := range cfg.Stages {
			if stage.ID != stageID || stage.Agent == "" {
				continue
			}
			if err := s.appendRunEventRequired(runID, stageAgentRequestedEvent, time.Now().UTC(), map[string]any{
				"stage_id": stageID, "actor_id": actorID, "via": "resume",
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
