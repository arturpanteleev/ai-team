package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_wave.go — adapter between the stage executor and the group scheduler
// (pkg/gatesched). The scheduler owns ordering, parallelism, durable attempts
// and recovery; this file supplies what only the pipeline knows: how a gate
// kind is executed (deterministic checker, reviewer agent), how the result
// version is hashed and frozen, and how the outcome is projected onto the
// legacy report/approval flow.

// automaticGateGroups is the part of a stage's chain the controller executes
// before the transition approval: every gate except human_approval, which is
// the transition approval itself. Groups left empty by that are dropped.
func automaticGateGroups(stage config.TemplateStage) workflow.GateGroups {
	var groups workflow.GateGroups
	for _, group := range stage.EffectiveGateGroups() {
		var kept workflow.GateGroup
		for _, ref := range group {
			if ref.Gate != gatelib.GateHumanApproval {
				kept = append(kept, ref)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, kept)
		}
	}
	return groups
}

// resultVersion is the hash of the result version a wave checks: the SHA-256
// of every declared output (name and content), in a stable order. Two attempts
// that produced the same bytes have the same version; any edit gives a new one.
func (rs *runState) resultVersion(result notifier.StageResult) (string, error) {
	return resultVersionOf(result.Outputs)
}

func resultVersionOf(outputs []runtime.Artifact) (string, error) {
	type entry struct{ name, path, digest string }
	entries := make([]entry, 0, len(outputs))
	for _, output := range outputs {
		if output.Path == "" {
			continue
		}
		digest := "unreadable"
		if data, err := safeio.ReadRegularFile(output.Path, maxArtifactFileBytes); err == nil {
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
		}
		entries = append(entries, entry{name: output.Name, path: output.Path, digest: digest})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return entries[i].path < entries[j].path
	})
	hash := sha256.New()
	for _, item := range entries {
		hash.Write([]byte(item.name + "\x00" + item.digest + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// resultFreezer pins the result version for a wave. The pipeline cannot take
// an operating-system write lock on files other processes own, so the freeze
// is detective and fail-closed: the hash is recomputed before the wave, after
// every gate and at the end, and any difference voids the wave.
type resultFreezer struct {
	outputs []runtime.Artifact
}

func (f resultFreezer) Freeze(_ context.Context, key gatesched.WaveKey) (gatesched.Lease, error) {
	lease := resultLease{outputs: f.outputs, sha: key.ArtifactSHA256}
	if err := lease.Verify(); err != nil {
		return nil, err
	}
	return lease, nil
}

type resultLease struct {
	outputs []runtime.Artifact
	sha     string
}

func (l resultLease) Verify() error {
	current, err := resultVersionOf(l.outputs)
	if err != nil {
		return err
	}
	if current != l.sha {
		return fmt.Errorf("%w (было %s, стало %s)", gatesched.ErrResultChanged, short(l.sha), short(current))
	}
	return nil
}

func (resultLease) Release() {}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// waveState is what the runners of one wave share.
type waveState struct {
	hard *hardWaveState
	// agentMu serializes reviewer agents: they share per-stage input
	// bookkeeping and attempt numbering of the run state.
	agentMu sync.Mutex

	mu      sync.Mutex
	reports map[string]stagecheck.Report // by gate instance name, this call only
	waitErr error                        // approval a waiting gate needs (grill)
	fatal   error                        // error that must abort the run (grill)
}

func (w *waveState) setReport(name string, report stagecheck.Report) {
	w.mu.Lock()
	w.reports[name] = report
	w.mu.Unlock()
}

func (w *waveState) setWait(err error) {
	w.mu.Lock()
	if w.waitErr == nil {
		w.waitErr = err
	}
	w.mu.Unlock()
}

func (w *waveState) setFatal(err error) {
	w.mu.Lock()
	if w.fatal == nil {
		w.fatal = err
	}
	w.mu.Unlock()
}

// waveOutcome is the pipeline view of a finished wave.
type waveOutcome struct {
	Result gatesched.Result
	Report stagecheck.Report
}

// Summary is the compact per-gate record written next to the legacy check
// event, so the check event alone shows which gate ended how.
func (o waveOutcome) Summary() []map[string]any {
	out := make([]map[string]any, 0, len(o.Result.Attempts))
	for _, attempt := range o.Result.Attempts {
		item := map[string]any{
			"gate": attempt.GateName, "group": attempt.Group, "execution": string(attempt.Execution),
			"attempt_id": attempt.AttemptID,
		}
		if attempt.Verdict != "" {
			item["verdict"] = string(attempt.Verdict)
		}
		out = append(out, item)
	}
	return out
}

func (rs *runState) gateJournal() *gatesched.EventJournal {
	return gatesched.NewEventJournal(gatesched.EventSink{
		Read:      rs.readEvidenceEvents,
		Append:    rs.evidence.Append,
		Projected: rs.recordTypedEvent,
	})
}

// runGateWave executes the automatic chain of the stage over the result
// version sha. It returns an error only when the wave could not be carried
// out reliably or a gate needs the run to stop (a grill question, a failure
// of the grill machinery); gate verdicts are in the outcome.
func (rs *runState) runGateWave(ctx context.Context, stage config.TemplateStage, result notifier.StageResult, groups workflow.GateGroups, sha string) (waveOutcome, error) {
	library := rs.p.cfg.GateLibrary()
	definitions := map[string]workflow.GateDefinition{}
	for _, ref := range groups.Refs() {
		definition, err := library.Lookup(ref.Gate, ref.Version)
		if err != nil {
			return waveOutcome{}, fmt.Errorf("этап %s: %w", stage.ID, err)
		}
		definitions[ref.Key()] = definition
	}
	visit := rs.visits[stage.ID]
	if visit < 1 {
		visit = 1
	}
	scope := result.AttemptID
	if scope == "" {
		scope = fmt.Sprintf("%s-visit-%d", stage.ID, visit)
	}
	state := &waveState{hard: &hardWaveState{}, reports: map[string]stagecheck.Report{}}
	runners := map[workflow.GateKind]gatesched.Runner{
		workflow.GateKindBuiltin: rs.builtinGateRunner(stage, result, state),
		workflow.GateKindLLM:     rs.agentGateRunner(stage, result, state),
	}
	scheduler, err := gatesched.New(rs.gateJournal(), runners, gatesched.Options{
		MaxParallel: rs.p.cfg.GateConcurrency,
		Clock:       func() time.Time { return rs.p.clock().UTC() },
		Freezer:     resultFreezer{outputs: result.Outputs},
	})
	if err != nil {
		return waveOutcome{}, fmt.Errorf("планировщик гейтов этапа %s: %w", stage.ID, err)
	}
	res, err := scheduler.Run(ctx, gatesched.Wave{
		Key:    gatesched.WaveKey{TaskID: rs.runID, Stage: stage.ID, Visit: visit, Scope: scope, ArtifactSHA256: sha},
		Groups: groups, Definitions: definitions,
	})
	if state.fatal != nil {
		return waveOutcome{}, state.fatal
	}
	if err != nil {
		if errors.Is(err, gatesched.ErrResultChanged) {
			return waveOutcome{}, fmt.Errorf("этап %s: результат изменён во время проверки, проверка отменена: %w", stage.ID, err)
		}
		return waveOutcome{}, fmt.Errorf("гейты этапа %s: %w", stage.ID, err)
	}
	if res.Status == gatesched.StatusWaiting {
		if state.waitErr != nil {
			return waveOutcome{}, state.waitErr
		}
		return waveOutcome{}, fmt.Errorf("этап %s: гейт ожидает решения, но запрос не создан", stage.ID)
	}
	return waveOutcome{Result: res, Report: rs.waveReport(stage, state, res)}, nil
}

// builtinGateRunner executes the deterministic code gates.
func (rs *runState) builtinGateRunner(stage config.TemplateStage, result notifier.StageResult, state *waveState) gatesched.Runner {
	return gatesched.RunnerFunc(func(ctx context.Context, in gatesched.Invocation) gatesched.Outcome {
		var sections, rules []string
		switch in.Ref.Gate {
		case gatelib.GateRequiredSections:
			sections = gatelib.StringListParam(in.Ref.Params, "sections")
		case gatelib.GateVerdictMarker, gatelib.GatePRExists, gatelib.GatePROpen, gatelib.GatePRBaseBranch,
			gatelib.GateProjectChecks, gatelib.GatePRChecksGreen:
			rules = []string{in.Ref.Gate}
		default:
			return gatesched.TechnicalError(fmt.Sprintf("нет реализации кодового гейта %s", in.Ref.Key()))
		}
		report := rs.runHardRules(ctx, stage, result, sections, rules, state.hard)
		state.setReport(in.Ref.InstanceName(), report)
		return outcomeFromReport(ctx, report)
	})
}

// agentGateRunner executes LLM gates: a plain review is one agent call whose
// report must carry a verdict marker; mode grill runs the question rounds of
// the existing grill machinery and waits for answers.
func (rs *runState) agentGateRunner(stage config.TemplateStage, result notifier.StageResult, state *waveState) gatesched.Runner {
	return gatesched.RunnerFunc(func(ctx context.Context, in gatesched.Invocation) gatesched.Outcome {
		check := config.AgentCheckOf(in.Ref)
		gateStage := stage
		gateStage.Check = &check
		state.agentMu.Lock()
		defer state.agentMu.Unlock()
		if check.Mode == grillMode {
			err := rs.runStageGrill(ctx, gateStage, result)
			var needsAnswer *ApprovalRequiredError
			switch {
			case err == nil:
				return gatesched.Pass("вопросы проверяющего закрыты")
			case errors.As(err, &needsAnswer):
				state.setWait(err)
				return gatesched.Waiting("ожидает ответа на вопросы проверяющего")
			default:
				state.setFatal(err)
				return gatesched.TechnicalError(err.Error())
			}
		}
		report := rs.runAgentCheck(ctx, gateStage, result)
		state.setReport(in.Ref.InstanceName(), report)
		return outcomeFromReport(ctx, report)
	})
}

// outcomeFromReport maps a checker report to a gate outcome. An error report
// (tool failure, no verdict from the reviewer, unavailable PR lookup) is a
// technical failure and never a verdict; a context that ended is canceled.
func outcomeFromReport(ctx context.Context, report stagecheck.Report) gatesched.Outcome {
	reason := reportReason(report)
	switch report.Status {
	case stagecheck.StatusError:
		if ctx.Err() != nil {
			return gatesched.Outcome{Execution: workflow.GateExecutionCanceled, Reason: reason}
		}
		return gatesched.TechnicalError(reason)
	case stagecheck.StatusFailed:
		return gatesched.Fail(reason)
	}
	if len(report.Rules) > 0 {
		skipped := true
		for _, rule := range report.Rules {
			if rule.Status != stagecheck.RuleSkipped {
				skipped = false
				break
			}
		}
		if skipped {
			return gatesched.Outcome{Execution: workflow.GateExecutionSucceeded, Verdict: workflow.GateVerdictNotApplicable, Reason: reason}
		}
	}
	return gatesched.Pass(reason)
}

func reportReason(report stagecheck.Report) string {
	var parts []string
	for _, rule := range report.Rules {
		if rule.Status == stagecheck.RulePassed || (rule.Status == stagecheck.RuleSkipped && rule.Reason == "") {
			continue
		}
		part := rule.Rule
		if rule.Reason != "" {
			part += ": " + rule.Reason
		}
		parts = append(parts, part)
	}
	if notes := strings.TrimSpace(report.Notes); notes != "" {
		parts = append(parts, notes)
	}
	return strings.Join(parts, "; ")
}

// waveReport builds the legacy stage-check report from the wave. A chain of one
// gate that ran in this call keeps that gate's report verbatim; otherwise the
// rule results of the gates are concatenated in chain order (gates reused from
// an earlier run of the same wave contribute their recorded verdict).
func (rs *runState) waveReport(stage config.TemplateStage, state *waveState, res gatesched.Result) stagecheck.Report {
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(res.Attempts) == 1 {
		if report, ok := state.reports[res.Attempts[0].GateName]; ok {
			report.Stage = stage.ID
			return report
		}
	}
	report := stagecheck.Report{Stage: stage.ID, Kind: stageCheckKind(stage)}
	if len(res.Blocking) > 0 {
		// The kind names what blocked: a failed code gate is a hard check even
		// when the chain also has a reviewer that never started.
		report.Kind = stagecheck.KindHard
		for _, blocked := range res.Blocking {
			if blocked.Executor == workflow.GateKindLLM {
				report.Kind = stagecheck.KindAgent
			}
		}
	}
	var notes []string
	status := stagecheck.StatusPassed
	for _, attempt := range res.Attempts {
		detailed, fresh := state.reports[attempt.GateName]
		switch {
		case fresh && detailed.Kind == stagecheck.KindAgent:
			report.Rules = append(report.Rules, stagecheck.RuleResult{Rule: attempt.GateName, Status: ruleStatus(attempt), Reason: attempt.Reason})
			report.Verdict = detailed.Verdict
		case fresh:
			report.Rules = append(report.Rules, detailed.Rules...)
			if detailed.PRIdentity != nil {
				report.PRIdentity = detailed.PRIdentity
			}
		default:
			report.Rules = append(report.Rules, stagecheck.RuleResult{Rule: attempt.GateName, Status: ruleStatus(attempt), Reason: attempt.Reason})
		}
		switch ruleStatus(attempt) {
		case "error":
			status = stagecheck.StatusError
		case stagecheck.RuleFailed:
			if status != stagecheck.StatusError {
				status = stagecheck.StatusFailed
			}
		}
	}
	if len(res.NotStarted) > 0 {
		notes = append(notes, "не запущены из-за отказа предыдущей группы: "+strings.Join(res.NotStarted, ", "))
	}
	report.Notes = strings.Join(notes, "\n")
	report.Status = status
	return report
}

func ruleStatus(attempt workflow.GateAttempt) string {
	switch attempt.Execution {
	case workflow.GateExecutionSucceeded:
		switch attempt.Verdict {
		case workflow.GateVerdictPass:
			return stagecheck.RulePassed
		case workflow.GateVerdictNotApplicable:
			return stagecheck.RuleSkipped
		default:
			return stagecheck.RuleFailed
		}
	default:
		return "error"
	}
}

// recordGatePins writes the exact gate definitions the task is bound to into
// the run evidence once, at the start of the task, and on every later start
// verifies that the library still has the same bytes for each pinned
// definition. The template snapshot already names exact versions; the digests
// prove that "id@version" still means what it meant when the task began.
func (rs *runState) recordGatePins() error {
	pins, err := rs.p.cfg.GatePins()
	if err != nil {
		return fmt.Errorf("закрепление определений гейтов: %w", err)
	}
	if len(pins) == 0 {
		return nil
	}
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return fmt.Errorf("чтение закреплённых определений гейтов: %w", err)
	}
	if found {
		for _, event := range events {
			if event.Type != gatesched.EventPinsRecorded {
				continue
			}
			return verifyRecordedPins(event, pins)
		}
	}
	data := make([]any, len(pins))
	for i, pin := range pins {
		data[i] = map[string]any{"gate": pin.Gate, "version": pin.Version, "kind": string(pin.Kind), "digest": pin.Digest}
	}
	eventData := map[string]any{"template": rs.p.cfg.Template, "pins": data}
	if len(rs.results) > 0 {
		eventData["late"] = true // a run that began before pins were recorded
	}
	if err := rs.evidence.Append(evidence.Event{Type: gatesched.EventPinsRecorded, Timestamp: time.Now().UTC(), Data: eventData}); err != nil {
		return fmt.Errorf("запись закреплённых определений гейтов: %w", err)
	}
	return nil
}

func verifyRecordedPins(event evidence.Event, current []workflow.GatePin) error {
	recorded, _ := event.Data["pins"].([]any)
	have := map[string]string{}
	for _, item := range recorded {
		pin, _ := item.(map[string]any)
		gate, _ := pin["gate"].(string)
		version, _ := pin["version"].(float64)
		digest, _ := pin["digest"].(string)
		have[fmt.Sprintf("%s@%d", gate, int(version))] = digest
	}
	for _, pin := range current {
		key := fmt.Sprintf("%s@%d", pin.Gate, pin.Version)
		digest, ok := have[key]
		if !ok {
			return fmt.Errorf("определение гейта %s не было закреплено при старте задачи", key)
		}
		if digest != pin.Digest {
			return fmt.Errorf("определение гейта %s изменилось с момента старта задачи (digest %s, закреплён %s): опубликованные определения неизменяемы", key, short(pin.Digest), short(digest))
		}
	}
	return nil
}
