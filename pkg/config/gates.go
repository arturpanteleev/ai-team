package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
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

// legacyGateView is the engine-facing projection of a stage's gate chain: the
// shape the current stage executor understands. It exists only until the
// group scheduler (S03) executes exit_gate_groups directly.
type legacyGateView struct {
	RequiredSections []string
	Check            *TemplateCheck
	Confirm          string
}

// gateClass orders what the legacy executor can run: deterministic code first,
// then one agent review, then the human confirmation.
type gateClass int

const (
	gateClassCode gateClass = iota
	gateClassLLM
	gateClassHuman
)

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
	if len(s.RequiredSections) > 0 {
		sections := make([]any, len(s.RequiredSections))
		for i, section := range s.RequiredSections {
			sections[i] = section
		}
		code = append(code, workflow.GateRef{Gate: gatelib.GateRequiredSections, Version: 1, Params: map[string]any{"sections": sections}})
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

// EffectiveGateGroups returns the stage's gate chain: the explicit
// exit_gate_groups when present, otherwise the translation of the legacy
// fields. It is what catalogs, validation and (later) the scheduler read.
func (s TemplateStage) EffectiveGateGroups() workflow.GateGroups {
	if len(s.ExitGateGroups) > 0 {
		return s.ExitGateGroups
	}
	return s.LegacyGateGroups()
}

// projectGates reduces an explicit gate chain to the legacy engine fields.
// It always returns a best-effort view (so a decoded stage has a consistent
// confirm value) together with an error when the chain uses something the
// pre-scheduler executor cannot run. Validate surfaces that error, so a
// template whose gates would be silently ignored never starts.
func projectGates(groups workflow.GateGroups) (legacyGateView, error) {
	var view legacyGateView
	var problems []string
	var rules []string
	var agent *TemplateCheck
	humanGates, agentGates, sectionGates := 0, 0, 0
	last := gateClassCode
	for i, group := range groups {
		groupClass := gateClass(-1)
		for j, ref := range group {
			path := fmt.Sprintf("exit_gate_groups[%d][%d]", i, j)
			class, ok := classOf(ref)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: гейт %s пока не исполняется контроллером (исполнение произвольных гейтов появится с планировщиком групп)", path, ref.Key()))
				continue
			}
			if ref.Mode.Effective() == workflow.GateModeAdvisory {
				problems = append(problems, fmt.Sprintf("%s: mode advisory пока не исполняется контроллером; используйте required", path))
			}
			if groupClass >= 0 && class != groupClass {
				problems = append(problems, fmt.Sprintf("exit_gate_groups[%d]: пока группа может содержать гейты только одного вида (код, LLM или человек)", i))
			}
			groupClass = class
			if class < last {
				problems = append(problems, fmt.Sprintf("%s: порядок групп пока фиксирован: код, затем LLM, затем человек", path))
			}
			last = class
			switch {
			case ref.Gate == gatelib.GateRequiredSections:
				sectionGates++
				view.RequiredSections = gatelib.StringListParam(ref.Params, "sections")
			case ruleGateIDs[ref.Gate]:
				rules = append(rules, ref.Gate)
			case ref.Gate == gatelib.GateAgentReview:
				agentGates++
				agent = &TemplateCheck{
					Kind: "agent", Agent: gatelib.StringParam(ref.Params, "agent"),
					Mode: gatelib.StringParam(ref.Params, "mode"), MaxRounds: gatelib.IntParam(ref.Params, "max_rounds"),
				}
			case ref.Gate == gatelib.GateHumanApproval:
				humanGates++
			}
		}
	}
	if sectionGates > 1 || agentGates > 1 || humanGates > 1 {
		problems = append(problems, "на этапе пока допускается не более одного required_sections, agent_review и human_approval")
	}
	if len(rules) > 0 && agent != nil {
		problems = append(problems, "правила кода (verdict_marker, pr_*) и agent_review на одном этапе пока не исполняются совместно")
	}
	switch {
	case agent != nil:
		view.Check = agent
	case len(rules) > 0:
		view.Check = &TemplateCheck{Kind: "hard", Rules: rules}
	}
	view.Confirm = "auto"
	if humanGates > 0 {
		view.Confirm = "required"
	}
	if len(problems) > 0 {
		return view, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return view, nil
}

func classOf(ref workflow.GateRef) (gateClass, bool) {
	if ref.Version != 1 {
		return 0, false
	}
	switch {
	case ref.Gate == gatelib.GateRequiredSections || ruleGateIDs[ref.Gate]:
		return gateClassCode, true
	case ref.Gate == gatelib.GateAgentReview:
		return gateClassLLM, true
	case ref.Gate == gatelib.GateHumanApproval:
		return gateClassHuman, true
	}
	return 0, false
}

// applyGateProjection makes a stage with explicit exit_gate_groups consistent
// for the engine: the legacy fields become the projection of the groups. A
// projection error is not reported here; Validate reports it.
func (s *TemplateStage) applyGateProjection() {
	view, _ := projectGates(s.ExitGateGroups)
	s.RequiredSections = view.RequiredSections
	s.Check = view.Check
	s.Confirm = view.Confirm
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

// validateGates checks every stage's gate chain against the library: known
// ID and exact version, declared parameters of the right type, applicability
// to the result, unique instance names, and — for explicit chains — that the
// chain is something the controller can execute and the legacy fields agree
// with it. Errors carry stages[i] (id) and the exit_gate_groups path.
func (c *Config) validateGates(add func(format string, args ...any)) {
	lib := c.library()
	for i, stage := range c.Stages {
		prefix := fmt.Sprintf("stages[%d] (%s)", i, stage.ID)
		if len(stage.ExitGateGroups) > 0 && c.SchemaVersion == LegacySchemaVersion {
			add("%s: exit_gate_groups требует schema_version %d (в v5 используйте required_sections, check, confirm)", prefix, CurrentSchemaVersion)
			continue
		}
		groups := stage.EffectiveGateGroups()
		_, errs := lib.ResolveGroups(groups, stage.gateStageContext())
		for _, err := range errs {
			add("%s: %v", prefix, err)
		}
		if len(stage.ExitGateGroups) == 0 || len(errs) > 0 {
			continue
		}
		view, err := projectGates(stage.ExitGateGroups)
		if err != nil {
			add("%s: %v", prefix, err)
			continue
		}
		if stage.Confirm != view.Confirm || !sameCheck(stage.Check, view.Check) || !sameStrings(stage.RequiredSections, view.RequiredSections) {
			add("%s: exit_gate_groups и поля check/required_sections/confirm рассогласованы; загрузите конфиг через ParseYAML или вызовите NormalizeGates", prefix)
		}
	}
}

// NormalizeGates makes explicit exit_gate_groups of every stage consistent
// with the engine projection. ParseYAML already does this; programmatic
// builders of v6 configs call it before Validate.
func (c *Config) NormalizeGates() {
	for i := range c.Stages {
		if len(c.Stages[i].ExitGateGroups) > 0 {
			c.Stages[i].applyGateProjection()
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameCheck(a, b *TemplateCheck) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Kind == b.Kind && a.Agent == b.Agent && a.Mode == b.Mode && a.MaxRounds == b.MaxRounds && sameStrings(a.Rules, b.Rules)
}

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
