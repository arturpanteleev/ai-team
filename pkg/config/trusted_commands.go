package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ErrCodeTrustedConfigOnly is the machine-readable code the web API returns
// when a template submitted through the web (REST, MCP, UI) names a host
// command, a container image, mounts or any other infrastructure setting that
// differs from the repository's file configuration.
const ErrCodeTrustedConfigOnly = "trusted_config_only"

// UntrustedCommandProblems lists what a template submitted through the web
// (validate/publish over REST, MCP or the UI) must not carry, as readable
// problems. The web template is a whole-document replacement, but only its
// process part may differ from the repository's file configuration
// (.ai-team/config.yaml, edited as code): everything else must be identical
// to the active (trusted) template. This is an allow-list (see
// configFieldClasses): a field that is not classified as process is compared,
// so a new configuration section is protected by default.
//
//   - gate references of kind external and docker (external_program,
//     docker_program) are rejected outright: their parameters are the command,
//     the image and the mounts;
//   - executor_adapters, checks and mcp_servers define commands too: a stage
//     may reference them by name, but each definition must be identical to the
//     same-named definition of the trusted template;
//   - every other infrastructure section (tracker endpoints and tokens, cicd,
//     notifications, containment, runtime, budget, redaction, ...) and the
//     delivery policy of a stage must equal the trusted one.
//
// trusted is the active template parsed from the repository, or nil when there
// is none; with nil every infrastructure section must be empty.
func (c *Config) UntrustedCommandProblems(trusted *Config) []string {
	return c.untrustedProblems(trusted, false)
}

// untrustedRunProblems is UntrustedCommandProblems for a template about to run
// rather than to be published: differences in tuningField fields do not count.
func (c *Config) untrustedRunProblems(trusted *Config) []string {
	return c.untrustedProblems(trusted, true)
}

func (c *Config) untrustedProblems(trusted *Config, forRun bool) []string {
	var problems []string
	registry := gatelib.Default()
	hasBaseline := trusted != nil
	if trusted == nil {
		trusted = &Config{}
	}
	trustedStages := map[string]TemplateStage{}
	for _, stage := range trusted.Stages {
		trustedStages[stage.ID] = stage
	}
	for _, stage := range c.Stages {
		// Exit and entry chains: a command gate (external/docker) can never be
		// introduced through the web, in either chain. The chains are matched
		// separately: a gate moved exit→entry (or back) checks a different
		// artifact and is not the same gate (finding C).
		trustedStage := trustedStages[stage.ID]
		chains := []struct{ want, have workflow.GateGroups }{
			{trustedStage.EffectiveGateGroups(), stage.EffectiveGateGroups()},
			{trustedStage.EntryGateGroups, stage.EntryGateGroups},
		}
		for _, pair := range chains {
			for _, ref := range pair.have.Refs() {
				if !isCommandGate(registry, ref) {
					continue
				}
				// A submission never carries a command gate. A stored template about
				// to run may, when the trusted file has the very same gate in the
				// very same chain of the same stage (its definitions are compared
				// below), so editing the file does not strand a run whose command came
				// from the file.
				if forRun && hasGateRef(pair.want, ref) {
					continue
				}
				problems = append(problems, fmt.Sprintf("этап %q: гейт %s запускает команду или контейнер, которых нет в файловом конфиге", stage.ID, ref.Gate))
			}
		}
	}
	problems = append(problems, c.workflowCommandGateProblems(trusted, registry, forRun)...)
	// Workflow gates are a process boundary, but unlike other tighten-only
	// fields they must also hold for a run: a pinned template may not drop or
	// weaken a workflow gate of the file configuration (finding B).
	problems = append(problems, c.workflowTightenProblems(trusted)...)
	// A stage ref that shares an instance name with a workflow gate replaces it
	// in the effective merged chain, so the workflow-only and stage-only checks
	// above miss a gate weakened through such an override. Compare the merged
	// chains of every stage of the file configuration as well.
	problems = append(problems, stageMergedChainTightenProblems(c, trusted, forRun)...)
	trustedAdapters, trustedChecks, trustedMCP := trustedDefinitions(trusted)
	for _, name := range sortedMapKeys(c.ExecutorAdapters) {
		if !reflect.DeepEqual(c.ExecutorAdapters[name], trustedAdapters[name]) {
			problems = append(problems, fmt.Sprintf("executor_adapters.%s: команда внешнего исполнителя не совпадает с адаптером файлового конфига", name))
		}
	}
	for _, check := range c.Checks {
		if want, ok := trustedChecks[check.Name]; !ok || !reflect.DeepEqual(check, want) {
			problems = append(problems, fmt.Sprintf("checks.%s: команда проверки не совпадает с проверкой файлового конфига", check.Name))
		}
	}
	for _, name := range sortedMapKeys(c.MCPServers) {
		if !reflect.DeepEqual(c.MCPServers[name], trustedMCP[name]) {
			problems = append(problems, fmt.Sprintf("mcp_servers.%s: команда MCP-сервера не совпадает с сервером файлового конфига", name))
		}
	}
	problems = append(problems, c.infrastructureProblems(trusted, hasBaseline, forRun)...)
	return problems
}

// fieldClass says whether a configuration field may be changed through the web.
type fieldClass int

const (
	// processField is a process setting: stages, routes, roles, visit limits,
	// status maps. A template submitted through the web may change it.
	processField fieldClass = iota + 1
	// trustedField is infrastructure: it must equal the file configuration.
	trustedField
	// derivedField is never read from YAML (yaml:"-") and is not compared.
	derivedField
	// definitionsField is compared per name by UntrustedCommandProblems
	// (executor_adapters, checks, mcp_servers).
	definitionsField
	// partialField mixes process and trusted parts and has a dedicated rule
	// (stages, tracker).
	partialField
	// tightenOnlyField may be changed through the web only towards a stricter
	// process: forbid_self_approval cannot be switched off, a return route
	// cannot be added, a visit limit cannot be raised (see tightenProblems).
	tightenOnlyField
	// tuningField is infrastructure for the web (it must equal the file
	// configuration) but only tuning: timeouts, model and effort. They cannot run
	// a command, reach a host or weaken isolation, so a pinned run whose
	// template differs from the file only there still runs (a paused task
	// keeps the limits it started with).
	tuningField
	// sameStageField is compared with the stage of the same id of the trusted
	// template (roles, executor, agent, adapter, MCP servers); a stage the
	// trusted template does not have may only be a human stage (see
	// stageProblems).
	sameStageField
)

// configFieldClasses is the allow-list of Config fields the web may change. A
// field absent from it fails TestConfigFieldClassification, so a new section
// cannot reach the web unclassified; at run time an unclassified field is
// compared like a trusted one.
var configFieldClasses = map[string]fieldClass{
	"QuestionStages": derivedField, "PipelineAgents": derivedField, "Workflow": derivedField,
	"SchemaVersion": processField, "Template": processField, "Title": processField,
	"StallAfter": processField, "ForbidSelfApproval": tightenOnlyField,
	"Stages": partialField, "Returns": tightenOnlyField, "MaxVisits": tightenOnlyField,
	"Checks": definitionsField, "MCPServers": definitionsField, "ExecutorAdapters": definitionsField,
	"CLI": trustedField, "Model": tuningField, "Effort": tuningField,
	"StageTimeout": tuningField, "GateConcurrency": tuningField,
	"PreflightTimeout": tuningField, "DeliveryTimeout": tuningField,
	"Containment": trustedField, "TreeHash": trustedField, "Budget": trustedField,
	"AutoPickup": trustedField, "Usage": trustedField, "CICD": trustedField,
	"Notifications": trustedField, "Redaction": trustedField, "Retention": trustedField,
	"Tracker": partialField,
}

// stageFieldClasses classifies TemplateStage fields. A stage may name another
// vetted adapter or MCP server (the definitions themselves are compared), but
// its delivery policy decides which controller checks guard commit/push/PR and
// must equal the trusted stage's.
var stageFieldClasses = map[string]fieldClass{
	"ID": processField, "Title": processField, "Function": sameStageField,
	"ApproverFunction": sameStageField, "Result": processField, "Executor": sameStageField,
	"Agent": sameStageField, "Adapter": sameStageField, "LinkKind": processField,
	"RequiredSections": processField, "Check": processField, "Confirm": tightenOnlyField,
	"ExitGateGroups": tightenOnlyField, "Skippable": tightenOnlyField, "MCPServers": sameStageField,
	"Delivery": trustedField,
}

// trackerFieldClasses classifies TrackerConfig fields: the status maps are
// process; the adapter, its endpoints, the dashboard address and the inbound
// rules (which let an external status drive the controller) are trusted.
var trackerFieldClasses = map[string]fieldClass{
	"Adapter": trustedField, "WebBaseURL": trustedField,
	"OnEnter": processField, "OnPass": processField,
	"Inbound": trustedField, "Fake": trustedField, "Jira": partialField,
}

// jiraFieldClasses classifies JiraConfig fields: only the legacy status map is
// process; the base URL, account and token variable stay with the file.
var jiraFieldClasses = map[string]fieldClass{
	"BaseURL": trustedField, "Email": trustedField, "TokenEnv": trustedField,
	"WebBaseURL": trustedField, "APIVersion": trustedField, "Auth": trustedField, "StageStatuses": processField,
}

func (c *Config) infrastructureProblems(trusted *Config, hasBaseline, forRun bool) []string {
	var problems []string
	mine, theirs := reflect.ValueOf(c).Elem(), reflect.ValueOf(trusted).Elem()
	typ := mine.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		switch class := configFieldClasses[field.Name]; {
		case class == processField, class == derivedField, class == definitionsField, class == partialField, class == tightenOnlyField:
		case class == tuningField && forRun:
		default:
			// trustedField, and any field nobody classified: protect by default.
			problems = append(problems, diffInfrastructure(yamlName(field), mine.Field(i), theirs.Field(i))...)
		}
	}
	problems = append(problems, trackerProblems(c.Tracker, trusted.Tracker)...)
	problems = append(problems, c.deliveryProblems(trusted)...)
	if !hasBaseline || forRun {
		// A template about to run is held to commands and infrastructure only:
		// roles, routes and limits are process the file may legitimately have
		// changed since the run was pinned. Without a file configuration there
		// is nothing to compare them with either; the infrastructure rules
		// above already refuse every section.
		return problems
	}
	problems = append(problems, c.stageProblems(trusted)...)
	problems = append(problems, c.tightenProblems(trusted)...)
	return problems
}

func subsetOf(items, of []string) bool {
	allowed := map[string]bool{}
	for _, item := range of {
		allowed[item] = true
	}
	for _, item := range items {
		if !allowed[item] {
			return false
		}
	}
	return true
}

// stageProblems enforces the sameStageField fields: a stage that exists in the
// trusted template keeps its roles, executor, agent, adapter and MCP servers;
// a stage the web adds may only be a human stage whose roles the trusted
// template already uses, so the web cannot introduce a new executor or a new
// approver role.
func (c *Config) stageProblems(trusted *Config) []string {
	var problems []string
	known := map[string]TemplateStage{}
	functions := map[string]bool{}
	for _, stage := range trusted.Stages {
		known[stage.ID] = stage
		functions[stage.Function] = true
		if stage.ApproverFunction != "" {
			functions[stage.ApproverFunction] = true
		}
	}
	for _, stage := range c.Stages {
		want, exists := known[stage.ID]
		if !exists {
			if stage.Agent != "" || stage.Adapter != "" || len(stage.MCPServers) > 0 || (stage.Executor != "" && stage.Executor != "human") {
				problems = append(problems, fmt.Sprintf("этап %q: новый этап из веба может быть только human без agent, adapter и mcp_servers", stage.ID))
			}
			for _, function := range []string{stage.Function, stage.ApproverFunction} {
				if function != "" && !functions[function] {
					problems = append(problems, fmt.Sprintf("этап %q: роль %q не используется ни одним этапом файлового конфига", stage.ID, function))
				}
			}
			continue
		}
		mine, theirs := reflect.ValueOf(stage), reflect.ValueOf(want)
		for name, class := range stageFieldClasses {
			if class != sameStageField {
				continue
			}
			a, b := mine.FieldByName(name), theirs.FieldByName(name)
			if isEmptyValue(a) && isEmptyValue(b) || reflect.DeepEqual(a.Interface(), b.Interface()) {
				continue
			}
			// An empty executor is a human stage.
			if name == "Executor" && effectiveExecutor(stage.Executor) == effectiveExecutor(want.Executor) {
				continue
			}
			// Dropping an adapter or narrowing the MCP servers makes the stage
			// less privileged (an internal executor, fewer tools): allowed.
			if name == "Adapter" && stage.Adapter == "" {
				continue
			}
			if name == "MCPServers" && subsetOf(stage.MCPServers, want.MCPServers) {
				continue
			}
			field, _ := reflect.TypeOf(stage).FieldByName(name)
			problems = append(problems, fmt.Sprintf("этап %q: %s не совпадает с этапом файлового конфига", stage.ID, yamlName(field)))
		}
		problems = append(problems, stageTightenProblems(stage, want)...)
	}
	return problems
}

func effectiveExecutor(executor string) string {
	if executor == "" {
		return "human"
	}
	return executor
}

// stageTightenProblems enforces the tighten-only stage fields of a stage that
// exists in the trusted template: it cannot become skippable, none of its
// non-command gates (exit and entry, including the confirmation of a
// transition) can be removed or changed, and no declared input artifact type
// can be dropped. Adding gates or input types only makes the stage stricter;
// command gates are refused separately. Deleting a whole stage stays allowed.
func stageTightenProblems(stage, want TemplateStage) []string {
	var problems []string
	if stage.Skippable && !want.Skippable {
		problems = append(problems, fmt.Sprintf("этап %q: skippable: из веба нельзя сделать пропускаемым этап, который в файловом конфиге обязателен", stage.ID))
	}
	registry := gatelib.Default()
	// Both the exit and the entry chain are tighten-only: a gate the file
	// configuration requires cannot be removed or weakened from the web. The
	// two chains are compared separately — a gate moved exit→entry (or back)
	// checks a different artifact and must be refused, not counted as kept
	// just because it appears in the other chain (finding C).
	for _, pair := range []struct {
		field      string
		want, have workflow.GateGroups
	}{
		{"exit_gate_groups", want.EffectiveGateGroups(), stage.EffectiveGateGroups()},
		{"entry_gate_groups", want.EntryGateGroups, stage.EntryGateGroups},
	} {
		for _, wantRef := range pair.want.Refs() {
			if isCommandGate(registry, wantRef) || hasGateRef(pair.have, wantRef) {
				continue
			}
			problems = append(problems, fmt.Sprintf("этап %q: %s: гейт %s (%s) файлового конфига нельзя убрать или ослабить из веба", stage.ID, pair.field, wantRef.InstanceName(), wantRef.Key()))
		}
	}
	// InputArtifacts declares which input types the entry gates validate.
	// Removing a type weakens that validation, so the web may only add types.
	for _, artifact := range want.InputArtifacts {
		kept := false
		for _, have := range stage.InputArtifacts {
			if have == artifact {
				kept = true
				break
			}
		}
		if !kept {
			problems = append(problems, fmt.Sprintf("этап %q: input_artifacts: тип %s файлового конфига нельзя убрать из веба", stage.ID, artifact))
		}
	}
	return problems
}

// hasGateRef reports whether groups use a gate semantically equal to ref: the
// same definition, version, instance name, mode and parameters. Callers pass a
// single chain so that exit and entry gates are never confused (finding C).
func hasGateRef(groups workflow.GateGroups, ref workflow.GateRef) bool {
	for _, have := range groups.Refs() {
		if sameGateRef(have, ref) {
			return true
		}
	}
	return false
}

// sameGateRef reports whether two references use the same definition, version,
// instance name, mode and parameters (the tighten/trusted comparison).
func sameGateRef(a, b workflow.GateRef) bool {
	return a.Gate == b.Gate && a.Version == b.Version && a.InstanceName() == b.InstanceName() &&
		a.Mode.Effective() == b.Mode.Effective() && sameParams(a.Params, b.Params)
}

// workflowCommandGateProblems refuses command gates (external/docker) reachable
// from the workflow block: the declared chains and the point overrides, and the
// inherited chain through resolution. The web may neither introduce such a gate
// into a workflow chain nor into an override; a stored template about to run
// keeps one only when the trusted file carries the same gate in its own
// effective workflow chains.
func (c *Config) workflowCommandGateProblems(trusted *Config, registry *gatelib.Registry, forRun bool) []string {
	var problems []string
	refs := c.workflowDeclaredRefs()
	if resolved, err := c.workflowGateRefs(); err == nil {
		refs = resolved
	}
	var trustedRefs []workflow.GateRef
	if trusted != nil {
		if resolved, err := trusted.workflowGateRefs(); err == nil {
			trustedRefs = resolved
		} else {
			trustedRefs = trusted.workflowDeclaredRefs()
		}
	}
	for _, ref := range refs {
		if !isCommandGate(registry, ref) {
			continue
		}
		if forRun && hasAnyGateRef(trustedRefs, ref) {
			continue
		}
		problems = append(problems, fmt.Sprintf("workflows: гейт %s запускает команду или контейнер, которых нет в файловом конфиге", ref.Gate))
	}
	return problems
}

// workflowDeclaredRefs lists every gate reference written directly in the
// workflow block: declared chains and point overrides, without inheritance.
func (c *Config) workflowDeclaredRefs() []workflow.GateRef {
	var refs []workflow.GateRef
	for _, wf := range c.AllWorkflows() {
		refs = append(refs, wf.EntryGateGroups.Refs()...)
		refs = append(refs, wf.ExitGateGroups.Refs()...)
		for _, ref := range wf.EntryGateOverrides {
			refs = append(refs, ref)
		}
		for _, ref := range wf.ExitGateOverrides {
			refs = append(refs, ref)
		}
	}
	return refs
}

func hasAnyGateRef(refs []workflow.GateRef, ref workflow.GateRef) bool {
	for _, have := range refs {
		if sameGateRef(have, ref) {
			return true
		}
	}
	return false
}

// hasGateInstanceName reports whether refs contain a gate with the same
// instance name as ref. A stage ref replaces a workflow gate by instance name
// in the merged chain, so this catches a stage override that changes the
// version or params of a workflow gate even when the full refs differ.
func hasGateInstanceName(refs []workflow.GateRef, ref workflow.GateRef) bool {
	name := ref.InstanceName()
	for _, have := range refs {
		if have.InstanceName() == name {
			return true
		}
	}
	return false
}

// workflowTightenProblems enforces the tighten-only semantics of the workflow
// block (finding B), for publish and run alike:
//
//   - every effective gate of every workflow declared in the file must survive
//     in the submitted workflow with the same instance, params and mode. The
//     comparison uses the resolved chains, so a weakened point override or a
//     removed inherited gate is caught;
//   - the active workflow cannot be switched to one whose effective chains do
//     not preserve every gate the file's active workflow applies to the run.
//
// Exit and entry chains are compared separately: moving a workflow gate between
// them checks a different artifact and is a weakening, not a preserved gate.
func (c *Config) workflowTightenProblems(trusted *Config) []string {
	var problems []string
	trustedByID := map[string]*WorkflowMeta{}
	for i := range trusted.Workflows {
		trustedByID[trusted.Workflows[i].ID] = &trusted.Workflows[i]
	}
	for _, id := range sortedMapKeys(trustedByID) {
		problems = append(problems,
			workflowChainTightenProblems(
				workflowEffectiveRefs(trusted, id, true), workflowEffectiveRefs(c, id, true),
				fmt.Sprintf("workflows[%s].entry_gate_groups", id))...)
		problems = append(problems,
			workflowChainTightenProblems(
				workflowEffectiveRefs(trusted, id, false), workflowEffectiveRefs(c, id, false),
				fmt.Sprintf("workflows[%s].exit_gate_groups", id))...)
	}
	problems = append(problems,
		workflowChainTightenProblems(
			workflowEffectiveRefs(trusted, trusted.ActiveWorkflowID(), true), workflowEffectiveRefs(c, c.ActiveWorkflowID(), true),
			"workflow (активный).entry_gate_groups")...)
	problems = append(problems,
		workflowChainTightenProblems(
			workflowEffectiveRefs(trusted, trusted.ActiveWorkflowID(), false), workflowEffectiveRefs(c, c.ActiveWorkflowID(), false),
			"workflow (активный).exit_gate_groups")...)
	return problems
}

// workflowChainTightenProblems lists effective refs of want that are missing
// (or weakened) in have. Ref equality includes instance name, params and mode.
func workflowChainTightenProblems(want, have []workflow.GateRef, path string) []string {
	var problems []string
	for _, ref := range want {
		if !hasAnyGateRef(have, ref) {
			problems = append(problems, fmt.Sprintf("%s: гейт %s (%s) файлового конфига нельзя убрать или ослабить из веба", path, ref.InstanceName(), ref.Key()))
		}
	}
	return problems
}

// workflowEffectiveRefs resolves a workflow's chain through inheritance and
// point overrides. A broken/unknown workflow contributes no refs, which the
// tighten comparison reports as dropped gates.
func workflowEffectiveRefs(cfg *Config, id string, entry bool) []workflow.GateRef {
	chain, err := cfg.WorkflowGateGroups(id, entry)
	if err != nil {
		return nil
	}
	return chain.Refs()
}

// stageMergedChain is the effective gate chain of a stage after the active
// workflow's gates are merged in (workflow inheritance, point overrides and
// same-name stage overrides all resolved). A broken chain contributes no refs,
// which the tighten comparison reports as dropped gates.
func stageMergedChain(cfg *Config, stage TemplateStage, entry bool) workflow.GateGroups {
	var (
		groups workflow.GateGroups
		err    error
	)
	if entry {
		groups, err = cfg.EntryGateGroupsFor(stage)
	} else {
		groups, err = cfg.ExitGateGroupsFor(stage)
	}
	if err != nil {
		return nil
	}
	return groups
}

// stageMergedChainTightenProblems closes the gap between the workflow-level and
// the stage-level tighten checks: a stage ref with the same instance name as a
// workflow gate replaces that gate in the effective merged chain (finding D).
// Comparing only the workflow's own chains and only the stage's own chains
// misses a web submission that weakens a workflow gate through such an override.
// Every stage of the file configuration is therefore compared on its effective
// merged chains, with the same ref equality used everywhere else (instance,
// params, mode and definition version).
//
// For a publish every trusted ref must survive. For a run only the refs that
// come from the trusted active workflow are protected: stage-level process
// gates may legitimately differ between a pinned run and a later file edit,
// while workflow gates must hold for a run too (finding B).
func stageMergedChainTightenProblems(submitted, trusted *Config, forRun bool) []string {
	var problems []string
	registry := gatelib.Default()
	trustedStages := make(map[string]TemplateStage, len(trusted.Stages))
	for _, stage := range trusted.Stages {
		trustedStages[stage.ID] = stage
	}
	submittedStages := make(map[string]TemplateStage, len(submitted.Stages))
	for _, stage := range submitted.Stages {
		submittedStages[stage.ID] = stage
	}
	workflowID := trusted.ActiveWorkflowID()
	workflowEntry := workflowEffectiveRefs(trusted, workflowID, true)
	workflowExit := workflowEffectiveRefs(trusted, workflowID, false)
	// workflowAll is the union of the trusted active workflow's effective entry
	// and exit refs. It must be available in both publish and run modes: a
	// workflow command gate must not be skipped by the merged-chain check below,
	// because a stage ref with the same instance name replaces it in the
	// effective chain and would otherwise silently drop the command gate.
	workflowAll := append(append([]workflow.GateRef(nil), workflowEntry...), workflowExit...)
	for _, id := range sortedMapKeys(trustedStages) {
		submittedStage, ok := submittedStages[id]
		if !ok {
			continue // deleting a whole stage stays allowed
		}
		wantStage := trustedStages[id]
		for _, pair := range []struct {
			field        string
			want, have   workflow.GateGroups
			workflowRefs []workflow.GateRef
		}{
			{"exit_gate_groups", stageMergedChain(trusted, wantStage, false), stageMergedChain(submitted, submittedStage, false), workflowExit},
			{"entry_gate_groups", stageMergedChain(trusted, wantStage, true), stageMergedChain(submitted, submittedStage, true), workflowEntry},
		} {
			for _, ref := range pair.want.Refs() {
				// Command gates (external_program, docker_program) are already
				// refused/protected by workflowCommandGateProblems and the stage
				// command-gate check; skip them here so name-only references to
				// trusted command definitions stay accepted (as stageTightenProblems
				// always did). A command gate that belongs to the workflow chain is
				// not skipped: a stage ref with the same instance name replaces it
				// in the effective merged chain, so the command gate must survive
				// that comparison too.
				if isCommandGate(registry, ref) && !hasGateInstanceName(workflowAll, ref) {
					continue
				}
				// For a run only refs belonging to the trusted active workflow are
				// protected (finding B). A stage ref replaces a workflow gate by
				// instance name, so a stage override may use a different version or
				// params; match by instance name, not the full ref, or a saved run
				// could silently weaken the effective workflow gate (P2 from review).
				if forRun && !hasGateInstanceName(pair.workflowRefs, ref) {
					continue
				}
				if !hasGateRef(pair.have, ref) {
					problems = append(problems, fmt.Sprintf("этап %q: %s: гейт %s (%s) файлового конфига нельзя убрать или ослабить из веба", id, pair.field, ref.InstanceName(), ref.Key()))
				}
			}
		}
	}
	return problems
}

func sameParams(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// tightenProblems enforces the tightenOnlyField fields: through the web the
// process can only get stricter than the file configuration.
func (c *Config) tightenProblems(trusted *Config) []string {
	var problems []string
	if trusted.ForbidSelfApproval && !c.ForbidSelfApproval {
		problems = append(problems, "forbid_self_approval: из веба нельзя выключить запрет самоподтверждения, включённый в файловом конфиге")
	}
	routes := map[[2]string]bool{}
	for _, route := range trusted.Returns {
		routes[[2]string{route.From, route.To}] = true
	}
	targets := map[string]bool{}
	for _, route := range c.Returns {
		targets[route.To] = true
		if !routes[[2]string{route.From, route.To}] {
			problems = append(problems, fmt.Sprintf("returns: маршрут %s → %s отсутствует в файловом конфиге", route.From, route.To))
		}
	}
	for _, id := range sortedMapKeys(targets) {
		if limit, allowed := c.visitLimit(id), trusted.visitLimit(id); limit > allowed {
			problems = append(problems, fmt.Sprintf("max_visits[%s]: лимит %d выше лимита файлового конфига (%d)", id, limit, allowed))
		}
	}
	return problems
}

// visitLimit is the effective visit limit of a return target, as compiled into
// the graph: an explicit max_visits entry, else the route limit, else 3.
func (c *Config) visitLimit(id string) int {
	if visits := c.MaxVisits[id]; visits > 0 {
		return visits
	}
	for _, route := range c.Returns {
		if route.To == id {
			if route.MaxVisits > 0 {
				return route.MaxVisits
			}
			return 3
		}
	}
	return 0
}

func trackerProblems(submitted, trusted *TrackerConfig) []string {
	// A missing tracker section and an empty one (or one with only an empty
	// jira) are the same configuration.
	view := func(t *TrackerConfig) *TrackerConfig {
		if t == nil {
			return nil
		}
		copied := zeroFields(t, trackerFieldClasses, processField)
		if copied.Jira != nil {
			copied.Jira = zeroFields(copied.Jira, jiraFieldClasses, processField)
		}
		return copied
	}
	return diffInfrastructure("tracker", reflect.ValueOf(view(submitted)), reflect.ValueOf(view(trusted)))
}

func (c *Config) deliveryProblems(trusted *Config) []string {
	var problems []string
	trustedDelivery := map[string]*TemplateDelivery{}
	for _, stage := range trusted.Stages {
		trustedDelivery[stage.ID] = stage.Delivery
	}
	for _, stage := range c.Stages {
		if stage.Delivery == nil {
			continue
		}
		if want, known := trustedDelivery[stage.ID]; !known || !reflect.DeepEqual(stage.Delivery, want) {
			problems = append(problems, fmt.Sprintf("этап %q: политика delivery не совпадает с этапом файлового конфига", stage.ID))
		}
	}
	// Dropping the policy altogether is allowed: a stage without delivery does
	// not commit, push or open a PR, so nothing is delivered with fewer checks.
	return problems
}

// zeroFields returns a shallow copy of value with the fields of the given
// class set to their zero value.
func zeroFields[T any](value *T, classes map[string]fieldClass, class fieldClass) *T {
	copied := *value
	rv := reflect.ValueOf(&copied).Elem()
	for name, got := range classes {
		if got == class {
			field := rv.FieldByName(name)
			field.Set(reflect.Zero(field.Type()))
		}
	}
	return &copied
}

func yamlName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	if name == "" || name == "-" {
		return strings.ToLower(field.Name)
	}
	return name
}

// diffInfrastructure lists the dotted yaml paths at which two values differ.
// Empty slices, maps and nil pointers mean the same configuration.
func diffInfrastructure(path string, a, b reflect.Value) []string {
	a, b = indirect(a), indirect(b)
	aEmpty, bEmpty := isEmptyValue(a), isEmptyValue(b)
	if aEmpty && bEmpty {
		return nil
	}
	bothStructs := a.IsValid() && b.IsValid() && a.Kind() == reflect.Struct && a.Type() == b.Type()
	if !bothStructs {
		if !aEmpty && !bEmpty && a.IsValid() && b.IsValid() && reflect.DeepEqual(a.Interface(), b.Interface()) {
			return nil
		}
		return []string{path + ": инфраструктурная настройка не совпадает с файловым конфигом"}
	}
	var out []string
	for i := 0; i < a.NumField(); i++ {
		field := a.Type().Field(i)
		if field.IsExported() {
			out = append(out, diffInfrastructure(path+"."+yamlName(field), a.Field(i), b.Field(i))...)
		}
	}
	return out
}

func indirect(v reflect.Value) reflect.Value {
	for (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

// isEmptyValue reports whether v carries no configuration: nil, empty and
// zero values are all the same, and a struct is empty when every exported
// field is, so an absent section equals a present but zero one.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		// A pointer to a scalar is a deliberate value (false differs from
		// unset); only a struct behind it can be "empty".
		return v.IsNil() || (v.Elem().Kind() == reflect.Struct && isEmptyValue(v.Elem()))
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !isEmptyValue(v.Field(i)) {
				return false
			}
		}
		return true
	}
	return v.IsZero()
}

func isCommandGate(registry *gatelib.Registry, ref workflow.GateRef) bool {
	if ref.Gate == gatelib.GateExternalProgram || ref.Gate == gatelib.GateDockerProgram {
		return true
	}
	def, err := registry.Lookup(ref.Gate, ref.Version)
	if err != nil {
		return false
	}
	return def.Kind == workflow.GateKindExternal || def.Kind == workflow.GateKindDocker
}

func trustedDefinitions(trusted *Config) (adapters map[string]any, checkDefs map[string]any, mcp map[string]any) {
	adapters, checkDefs, mcp = map[string]any{}, map[string]any{}, map[string]any{}
	if trusted == nil {
		return
	}
	for name, spec := range trusted.ExecutorAdapters {
		adapters[name] = spec
	}
	for _, check := range trusted.Checks {
		checkDefs[check.Name] = check
	}
	for name, server := range trusted.MCPServers {
		mcp[name] = server
	}
	return
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
