package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Inbound action names of tracker.inbound.rules[].action.
const (
	TrackerInboundAdvance = "advance"
	TrackerInboundReturn  = "return"
)

// Defaults and limits of the inbound poll.
const (
	DefaultTrackerInboundPollInterval = time.Minute
	MinTrackerInboundPollInterval     = 5 * time.Second
)

// TrackerInboundConfig turns tracker status changes into requests to the
// controller. The map is explicit: a status that no rule names does nothing.
// A rule is a request, never a command: the controller still enforces the
// route, gates, human confirmations and visit limits.
//
//	tracker:
//	  inbound:
//	    poll_interval: 30s
//	    rules:
//	      - status: "In Progress"        # external status, case-insensitive
//	        action: advance              # start/continue the current stage
//	        stage: implementation        # optional: only at this stage
//	      - status: "Rework"
//	        action: return               # ask for a configured return route
//	        stage: code_review
//	        to_stage: implementation
type TrackerInboundConfig struct {
	// PollInterval is how often the tracker is polled (default 1m, min 5s).
	PollInterval string               `yaml:"poll_interval,omitempty"`
	Rules        []TrackerInboundRule `yaml:"rules,omitempty"`
}

// TrackerInboundRule maps one external status to one stage request.
type TrackerInboundRule struct {
	Status  string `yaml:"status"`
	Action  string `yaml:"action"`
	Stage   string `yaml:"stage,omitempty"`
	ToStage string `yaml:"to_stage,omitempty"`
}

// EffectivePollInterval returns the configured period or the default.
func (ic *TrackerInboundConfig) EffectivePollInterval() time.Duration {
	if ic == nil || strings.TrimSpace(ic.PollInterval) == "" {
		return DefaultTrackerInboundPollInterval
	}
	duration, err := time.ParseDuration(strings.TrimSpace(ic.PollInterval))
	if err != nil || duration < MinTrackerInboundPollInterval {
		return DefaultTrackerInboundPollInterval
	}
	return duration
}

func (ic *TrackerInboundConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{"poll_interval": true, "rules": true}, "config: tracker inbound"); err != nil {
		return err
	}
	type plain TrackerInboundConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*ic = TrackerInboundConfig(decoded)
	return nil
}

func (rule *TrackerInboundRule) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{"status": true, "action": true, "stage": true, "to_stage": true}, "config: tracker inbound rule"); err != nil {
		return err
	}
	type plain TrackerInboundRule
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*rule = TrackerInboundRule(decoded)
	return nil
}

// validateInbound returns problems of the inbound map. An identical
// (status, stage) pair named twice is rejected here; overlap between a rule
// without a stage and a stage-specific one is legal and is diagnosed as an
// ambiguity at runtime, when a status actually arrives.
func (ic *TrackerInboundConfig) validateInbound(stageIDs map[string]bool) []string {
	if ic == nil {
		return nil
	}
	var problems []string
	if text := strings.TrimSpace(ic.PollInterval); text != "" {
		duration, err := time.ParseDuration(text)
		if err != nil || duration < MinTrackerInboundPollInterval {
			problems = append(problems, fmt.Sprintf("tracker.inbound.poll_interval должен быть длительностью не меньше %s", MinTrackerInboundPollInterval))
		}
	}
	seen := map[string]bool{}
	for index, rule := range ic.Rules {
		name := fmt.Sprintf("tracker.inbound.rules[%d]", index)
		status := strings.TrimSpace(rule.Status)
		if status == "" || strings.ContainsAny(status, "\r\n\x00") {
			problems = append(problems, name+".status должен содержать имя статуса трекера")
		}
		switch rule.Action {
		case TrackerInboundAdvance:
			if rule.ToStage != "" {
				problems = append(problems, name+".to_stage допустим только для action: return")
			}
		case TrackerInboundReturn:
			if rule.ToStage == "" {
				problems = append(problems, name+".to_stage обязателен для action: return")
			}
		default:
			problems = append(problems, fmt.Sprintf("%s.action должен быть advance или return, получено %q", name, rule.Action))
		}
		if rule.Stage != "" && !stageIDs[rule.Stage] {
			problems = append(problems, fmt.Sprintf("%s.stage содержит неизвестный этап %q", name, rule.Stage))
		}
		if rule.ToStage != "" && !stageIDs[rule.ToStage] {
			problems = append(problems, fmt.Sprintf("%s.to_stage содержит неизвестный этап %q", name, rule.ToStage))
		}
		key := strings.ToLower(status) + "\x00" + rule.Stage
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s: статус %q с этапом %q уже описан выше (неоднозначно)", name, status, rule.Stage))
		}
		seen[key] = true
	}
	return problems
}
