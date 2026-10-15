package tracker

import (
	"strings"
)

// Inbound actions a tracker status can request. They are requests, not
// commands: the controller decides whether the route is allowed.
const (
	// ActionAdvance asks the graph to move forward: start or continue the
	// current stage. It never confirms a human decision and never skips a gate.
	ActionAdvance = "advance"
	// ActionReturn asks to send the task back along a configured return route.
	ActionReturn = "return"
)

// InboundRule maps an external status to a stage request. Stage optionally
// limits the rule to one current stage; ToStage is the return target.
type InboundRule struct {
	Status  string
	Action  string
	Stage   string
	ToStage string
}

// Resolution is the outcome of mapping one status for the current stage.
type Resolution int

const (
	// ResolutionUnmapped means no rule names the status.
	ResolutionUnmapped Resolution = iota
	// ResolutionMatch means exactly one distinct request matches.
	ResolutionMatch
	// ResolutionAmbiguous means several different requests match, so the
	// status must not move the graph.
	ResolutionAmbiguous
)

// ResolveInbound finds the request for a status while the task is at
// currentStage. Status names are compared case-insensitively and trimmed.
// Rules that agree on action and target do not make a status ambiguous; any
// disagreement does, whatever the rule order (including a wildcard rule that
// overlaps a stage-specific one).
func ResolveInbound(rules []InboundRule, status, currentStage string) (InboundRule, Resolution, []InboundRule) {
	status = strings.TrimSpace(status)
	var matched []InboundRule
	for _, rule := range rules {
		if !strings.EqualFold(strings.TrimSpace(rule.Status), status) {
			continue
		}
		if rule.Stage != "" && rule.Stage != currentStage {
			continue
		}
		matched = append(matched, rule)
	}
	switch len(matched) {
	case 0:
		return InboundRule{}, ResolutionUnmapped, nil
	case 1:
		return matched[0], ResolutionMatch, matched
	}
	first := matched[0]
	for _, other := range matched[1:] {
		if other.Action != first.Action || other.ToStage != first.ToStage {
			return InboundRule{}, ResolutionAmbiguous, matched
		}
	}
	// Identical requests: prefer the stage-specific spelling for the record.
	for _, rule := range matched {
		if rule.Stage != "" {
			return rule, ResolutionMatch, matched
		}
	}
	return first, ResolutionMatch, matched
}

// IsOwnEcho reports whether a change was made by ai-team itself and must not
// become an inbound request.
func IsOwnEcho(change Change) bool { return change.Origin == OriginAITeam }
