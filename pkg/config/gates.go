package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/llmgate"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

// Schema versions of config.yaml.
//
//   - v5 (LegacySchemaVersion) expresses what a stage must satisfy through
//     three unrelated stage fields: required_sections, check and confirm.
//   - v6 (CurrentSchemaVersion) replaces all three with one explicit
//     exit_gate_groups list of gate references into the versioned gate
//     library (pkg/gatelib). The stage result type (result: md|link|approve)
//     is the only thing left that describes the result itself.
//
// v5 files keep loading unchanged through the compatibility loader: the
// legacy fields stay authoritative and are translated on demand by
// LegacyGateGroups, so a v5 `idea-to-prod` follows the same path and yields
// the same verdicts as its v6 translation (TestIdeaToProdV5AndV6AreEquivalent).
// The group scheduler (pkg/gatesched, wired in pkg/pipeline) executes the
// effective chain of either spelling; there is no engine-side projection of
// a v6 chain back to the legacy fields.
const (
	LegacySchemaVersion  = 5
	CurrentSchemaVersion = 6
	// GraphSchemaVersion is the version of the compiled workflow graph, which
	// does not depend on gates and is therefore unchanged by schema v6.
	GraphSchemaVersion = 5
)

// SupportedSchemaVersions lists the config schemas the loader accepts.
var SupportedSchemaVersions = []int{LegacySchemaVersion, CurrentSchemaVersion}

func supportedSchemaVersion(version int) bool {
	for _, supported := range SupportedSchemaVersions {
		if version == supported {
			return true
		}
	}
	return false
}

// legacyGateKeys are the stage keys schema v6 no longer accepts.
var legacyGateKeys = []string{"required_sections", "check", "confirm"}

// ruleGateIDs are the v1 code gates that map to hard-check rule names.
var ruleGateIDs = func() map[string]bool {
	ids := map[string]bool{gatelib.GateVerdictMarker: true}
	for _, id := range gatelib.PRRuleGates {
		ids[id] = true
	}
	return ids
}()

// LegacyGateGroups translates the schema-v5 fields of a stage into the gate
// chain they have always meant:
//
//	required_sections + hard check rules  -> one group of code gates
//	check kind agent                      -> agent_review@1 group
//	confirm required                      -> human_approval@1 group
//
// A stage without legacy gate fields yields no groups. Unknown hard-check rule
// names are carried over as gate references so that validation rejects them
// by name before the run instead of the executor failing at run time.
func (s TemplateStage) LegacyGateGroups() workflow.GateGroups {
	var groups workflow.GateGroups
	var code workflow.GateGroup
	if sections := normalizedSections(s.RequiredSections); len(sections) > 0 {
		list := make([]any, len(sections))
		for i, section := range sections {
			list[i] = section
		}
		code = append(code, workflow.GateRef{Gate: gatelib.GateRequiredSections, Version: 1, Params: map[string]any{"sections": list}})
	}
	if s.Check != nil && s.Check.Kind == "hard" {
		for _, rule := range s.Check.Rules {
			if rule == "required_sections" {
				continue // the explicit required_sections gate above covers it
			}
			code = append(code, workflow.GateRef{Gate: rule, Version: 1})
		}
	}
	if len(code) > 0 {
		groups = append(groups, code)
	}
	if s.Check != nil && s.Check.Kind == "agent" && s.Check.Agent != "" {
		params := map[string]any{"agent": s.Check.Agent}
		if s.Check.Mode != "" {
			params["mode"] = s.Check.Mode
		}
		if s.Check.MaxRounds > 0 {
			params["max_rounds"] = s.Check.MaxRounds
		}
		groups = append(groups, workflow.GateGroup{{Gate: gatelib.GateAgentReview, Version: 1, Params: params}})
	}
	confirm := s.Confirm
	if confirm == "" {
		confirm = defaultConfirmForResult(s.Result)
	}
	if confirm == "required" {
		groups = append(groups, workflow.GateGroup{{Gate: gatelib.GateHumanApproval, Version: 1}})
	}
	return groups
}

// normalizedSections trims the edges of every section title and drops exact
// duplicates after trimming. A v5 file that master accepted with padded titles
// (headings are matched case- and edge-insensitively anyway) must keep
// loading; the gate library itself stays strict for v6 files. Empty titles are
// kept so that the v5 validation reports them as it always did.
func normalizedSections(sections []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, section := range sections {
		trimmed := strings.TrimSpace(section)
		if trimmed != "" && seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// EffectiveGateGroups returns the stage's gate chain: the explicit
// exit_gate_groups when present, otherwise the translation of the legacy
// fields. It is what catalogs, validation and (later) the scheduler read.
func (s TemplateStage) EffectiveGateGroups() workflow.GateGroups {
	if len(s.ExitGateGroups) > 0 {
		return s.ExitGateGroups
	}
	return s.LegacyGateGroups()
}

// gateStageContext is the applicability context of a stage.
func (s TemplateStage) gateStageContext() gatelib.StageContext {
	return gatelib.StageContext{Result: s.Result, LinkKind: s.LinkKind}
}

// rejectLegacyGateKeys scans a schema-v6 stages sequence and rejects the keys
// that v5 used for gates, naming the path and what replaces them.
func rejectLegacyGateKeys(stages *yaml.Node) error {
	if stages == nil || stages.Kind != yaml.SequenceNode {
		return nil
	}
	replacement := map[string]string{
		"required_sections": "гейт required_sections@1 с параметром sections",
		"check":             "гейты verdict_marker@1, pr_exists@1, ... или agent_review@1",
		"confirm":           "гейт human_approval@1 в последней группе (нет гейта — нет подтверждения)",
	}
	for index, stage := range stages.Content {
		if stage.Kind != yaml.MappingNode {
			continue
		}
		id := ""
		for i := 0; i+1 < len(stage.Content); i += 2 {
			if stage.Content[i].Value == "id" {
				id = stage.Content[i+1].Value
			}
		}
		for i := 0; i+1 < len(stage.Content); i += 2 {
			key := stage.Content[i]
			if text, legacy := replacement[key.Value]; legacy {
				return fmt.Errorf("config: stages[%d] (%s) строка %d: ключ %q удалён в schema_version 6; замените на exit_gate_groups — %s (перенос v5→v6: docs/reference/config.md)", index, id, key.Line, key.Value, text)
			}
		}
	}
	return nil
}

// ValidateWithLibrary is Validate against an explicit gate library. Validate
// uses the built-in library gatelib.Default().
func (c *Config) ValidateWithLibrary(reg AgentLookup, lib *gatelib.Registry) error {
	c.gateLibrary = lib
	defer func() { c.gateLibrary = nil }()
	return c.Validate(reg)
}

func (c *Config) library() *gatelib.Registry {
	if c.gateLibrary != nil {
		return c.gateLibrary
	}
	return gatelib.Default()
}

// legacyOrigin marks errors of a gate chain that was translated from the
// schema-v5 fields: the path in such an error points into a chain the file
// does not literally contain.
const legacyOrigin = " (из required_sections/check v5)"

// validateGates checks every stage's gate chain against the library: known
// ID and exact version, declared parameters of the right type, applicability
// to the result, unique instance names, and the constraints of the current
// executor. Errors carry stages[i] (id) and the exit_gate_groups path; for a
// v5 stage the path is marked as translated.
//
// A v5 stage is validated no stricter than before the gate library existed:
// the applicability of a rule to the result type is not checked for it (the
// rule then reports skipped/failed at run time exactly as it always did), and
// section titles are normalized by LegacyGateGroups.
func (c *Config) validateGates(reg AgentLookup, add func(format string, args ...any)) {
	lib := c.library()
	for i, stage := range c.Stages {
		prefix := fmt.Sprintf("stages[%d] (%s)", i, stage.ID)
		explicit := len(stage.ExitGateGroups) > 0
		if explicit && c.SchemaVersion == LegacySchemaVersion {
			add("%s: exit_gate_groups требует schema_version %d (в v5 используйте required_sections, check, confirm)", prefix, CurrentSchemaVersion)
			continue
		}
		if explicit && (len(stage.RequiredSections) > 0 || stage.Check != nil) {
			add("%s: exit_gate_groups и поля required_sections/check заданы одновременно; оставьте один способ (NormalizeGates очищает устаревшие поля)", prefix)
			continue
		}
		groups := stage.EffectiveGateGroups()
		context := stage.gateStageContext()
		suffix := ""
		if !explicit {
			context.Lenient = true
			suffix = legacyOrigin
		}
		_, errs := lib.ResolveGroups(groups, context)
		for _, err := range errs {
			add("%s: %v%s", prefix, err, suffix)
		}
		if len(errs) > 0 || !explicit {
			continue // a translated v5 chain is shaped by construction
		}
		c.validateChainConstraints(reg, prefix, groups, add)
	}
}

// validateChainConstraints enforces what the controller can honestly execute
// today. They are properties of the executor, not of the gate library:
//
//   - a human_approval gate is the transition decision of the stage: it is
//     requested only after every automatic gate of the chain ended, so it can
//     only sit in the last group; a gate that shares that group with
//     automatic ones simply waits for them (the human is asked after the
//     automatic gates of the group end). An automatic gate after the human
//     decision would need a second pause inside the transition and is not
//     offered (see docs/reference/config.md);
//   - agent_review with mode grill keeps its question rounds in per-stage
//     approvals, so one stage can have at most one;
//   - mode advisory is carried by the contract and honored by the scheduler,
//     but not yet offered to template authors (S06).
func (c *Config) validateChainConstraints(reg AgentLookup, prefix string, groups workflow.GateGroups, add func(format string, args ...any)) {
	grills := 0
	for i, group := range groups {
		for j, ref := range group {
			path := fmt.Sprintf("exit_gate_groups[%d][%d]", i, j)
			if ref.Mode.Effective() == workflow.GateModeAdvisory {
				add("%s: %s: mode advisory пока не доступен; используйте required", prefix, path)
			}
			switch ref.Gate {
			case gatelib.GateHumanApproval:
				if i != len(groups)-1 {
					add("%s: %s: human_approval допускается только в последней группе: решение человека — переход этапа, он запрашивается после всех автоматических гейтов, а гейт после решения человека не исполняется", prefix, path)
				}
			case gatelib.GateAgentReview:
				agent := gatelib.StringParam(ref.Params, "agent")
				if reg != nil && agent != "" && !reg.Exists(agent) {
					add("%s: %s: агент проверки %q не найден в registry", prefix, path, agent)
				}
				if gatelib.StringParam(ref.Params, "mode") == "grill" {
					grills++
					if grills > 1 {
						add("%s: %s: на этапе допускается не более одного agent_review с mode grill", prefix, path)
					}
					for _, name := range []string{"verdict_schema", "max_attempts"} {
						if _, set := ref.Params[name]; set {
							add("%s: %s: %s не применим к mode grill: у грилла свой протокол раундов (max_rounds)", prefix, path, name)
						}
					}
				} else if gatelib.IntParam(ref.Params, "max_rounds") > 0 {
					add("%s: %s: max_rounds допустим только для mode grill", prefix, path)
				}
				if attempts := gatelib.IntParam(ref.Params, "max_attempts"); attempts > llmgate.MaxAttempts {
					add("%s: %s: max_attempts %d больше допустимого %d", prefix, path, attempts, llmgate.MaxAttempts)
				}
			}
		}
	}
}

// NormalizeGates clears the legacy fields of every stage that declares an
// explicit exit_gate_groups chain, so that the chain is the only description
// of what the stage checks. Stages without an explicit chain are untouched:
// their legacy fields are the chain (see LegacyGateGroups).
func (c *Config) NormalizeGates() {
	for i := range c.Stages {
		if len(c.Stages[i].ExitGateGroups) > 0 {
			c.Stages[i].RequiredSections, c.Stages[i].Check, c.Stages[i].Confirm = nil, nil, ""
		}
	}
}

// SectionsRequired returns the section titles required by the chain: the
// union of every required_sections gate, in order.
func (s TemplateStage) SectionsRequired() []string {
	var out []string
	seen := map[string]bool{}
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ref.Gate != gatelib.GateRequiredSections {
			continue
		}
		for _, section := range gatelib.StringListParam(ref.Params, "sections") {
			if !seen[section] {
				seen[section] = true
				out = append(out, section)
			}
		}
	}
	return out
}

// CodeRules returns the rule gates of the chain (verdict_marker, pr_*,
// project_checks, pr_checks_green) in order.
func (s TemplateStage) CodeRules() []string {
	var out []string
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ruleGateIDs[ref.Gate] {
			out = append(out, ref.Gate)
		}
	}
	return out
}

// AgentChecks returns the LLM gates of the chain in order, in the form the
// agent-check runner consumes.
func (s TemplateStage) AgentChecks() []TemplateCheck {
	var out []TemplateCheck
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ref.Gate == gatelib.GateAgentReview {
			out = append(out, AgentCheckOf(ref))
		}
	}
	return out
}

// AgentCheckOf converts one agent_review reference to its check parameters.
func AgentCheckOf(ref workflow.GateRef) TemplateCheck {
	return TemplateCheck{
		Kind: "agent", Agent: gatelib.StringParam(ref.Params, "agent"),
		Mode: gatelib.StringParam(ref.Params, "mode"), MaxRounds: gatelib.IntParam(ref.Params, "max_rounds"),
		VerdictSchema: gatelib.StringParam(ref.Params, "verdict_schema"), MaxAttempts: gatelib.IntParam(ref.Params, "max_attempts"),
	}
}

// RejectStays reports whether a rejection of the stage's human_approval gate
// keeps the task at the stage (on_reject: stay) instead of ending the run.
func (s TemplateStage) RejectStays() bool {
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ref.Gate == gatelib.GateHumanApproval && gatelib.StringParam(ref.Params, "on_reject") == gatelib.OnRejectStay {
			return true
		}
	}
	return false
}

// HumanGateRef returns the stage's human_approval gate reference.
func (s TemplateStage) HumanGateRef() (workflow.GateRef, bool) {
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ref.Gate == gatelib.GateHumanApproval {
			return ref, true
		}
	}
	return workflow.GateRef{}, false
}

// GrillCheck returns the agent_review gate that runs in grill mode, if any.
func (s TemplateStage) GrillCheck() *TemplateCheck {
	for _, check := range s.AgentChecks() {
		if check.Mode == "grill" {
			check := check
			return &check
		}
	}
	return nil
}

// NeedsConfirmation reports whether the chain contains the human decision
// gate, i.e. whether leaving the stage forward requires an approval.
func (s TemplateStage) NeedsConfirmation() bool {
	for _, ref := range s.EffectiveGateGroups().Refs() {
		if ref.Gate == gatelib.GateHumanApproval {
			return true
		}
	}
	return false
}

// DisableConfirmation removes the human decision from the stage in either
// spelling (auto mode runs a template without human confirmations).
func (s *TemplateStage) DisableConfirmation() {
	s.Confirm = "auto"
	if len(s.ExitGateGroups) == 0 {
		return
	}
	var groups workflow.GateGroups
	for _, group := range s.ExitGateGroups {
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
	s.ExitGateGroups = groups
}

// GateLibrary returns the library the template is validated against and the
// scheduler resolves definitions from.
func (c *Config) GateLibrary() *gatelib.Registry { return c.library() }

// GatePins resolves every gate reference of the template to the exact
// definition it is bound to and returns the unique pins sorted by ID and
// version. A template and a task started from it use these definitions, and
// only these, even after the library publishes newer versions: the template
// YAML carries exact versions, the library keeps old versions forever, and
// the digest in the pin proves the definition body.
func (c *Config) GatePins() ([]workflow.GatePin, error) {
	lib := c.library()
	seen := map[string]workflow.GatePin{}
	for i, stage := range c.Stages {
		for _, ref := range stage.EffectiveGateGroups().Refs() {
			def, err := lib.Lookup(ref.Gate, ref.Version)
			if err != nil {
				return nil, fmt.Errorf("stages[%d] (%s): %w", i, stage.ID, err)
			}
			seen[def.Ref()] = gatelib.Pin(def)
		}
	}
	pins := make([]workflow.GatePin, 0, len(seen))
	for _, pin := range seen {
		pins = append(pins, pin)
	}
	sort.Slice(pins, func(i, j int) bool {
		if pins[i].Gate != pins[j].Gate {
			return pins[i].Gate < pins[j].Gate
		}
		return pins[i].Version < pins[j].Version
	})
	return pins, nil
}

// MigrateToCurrent returns a copy of the config rewritten as the current
// schema: legacy fields are translated into exit_gate_groups. It is the
// explicit v5→v6 migration used by the CLI/UI to offer an upgraded template;
// loading never rewrites a file by itself.
func (c *Config) MigrateToCurrent() *Config {
	migrated := *c
	migrated.SchemaVersion = CurrentSchemaVersion
	migrated.Stages = make([]TemplateStage, len(c.Stages))
	for i, stage := range c.Stages {
		stage.ExitGateGroups = stage.EffectiveGateGroups()
		migrated.Stages[i] = stage
	}
	migrated.NormalizeGates()
	return &migrated
}
