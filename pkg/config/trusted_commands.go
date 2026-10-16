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
		for _, ref := range stage.EffectiveGateGroups().Refs() {
			if !isCommandGate(registry, ref) {
				continue
			}
			// A submission never carries a command gate. A stored template about
			// to run may, when the trusted file has the very same gate in the
			// very same stage (its definitions are compared below), so editing
			// the file does not strand a run whose command came from the file.
			if forRun && hasGateRef(trustedStages[stage.ID], ref) {
				continue
			}
			problems = append(problems, fmt.Sprintf("этап %q: гейт %s запускает команду или контейнер, которых нет в файловом конфиге", stage.ID, ref.Gate))
		}
	}
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
	"WebBaseURL": trustedField, "APIVersion": trustedField, "StageStatuses": processField,
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
// exists in the trusted template: it cannot become skippable, and none of its
// non-command exit gates (which include the confirmation of a transition) can
// be removed or changed. Adding gates only makes the stage stricter; command
// gates are refused separately. Deleting a whole stage stays allowed.
func stageTightenProblems(stage, want TemplateStage) []string {
	var problems []string
	if stage.Skippable && !want.Skippable {
		problems = append(problems, fmt.Sprintf("этап %q: skippable: из веба нельзя сделать пропускаемым этап, который в файловом конфиге обязателен", stage.ID))
	}
	registry := gatelib.Default()
	for _, wantRef := range want.EffectiveGateGroups().Refs() {
		if isCommandGate(registry, wantRef) || hasGateRef(stage, wantRef) {
			continue
		}
		problems = append(problems, fmt.Sprintf("этап %q: гейт %s (%s) файлового конфига нельзя убрать или ослабить из веба", stage.ID, wantRef.InstanceName(), wantRef.Key()))
	}
	return problems
}

// hasGateRef reports whether stage uses a gate semantically equal to ref: the
// same definition, version, instance name, mode and parameters.
func hasGateRef(stage TemplateStage, ref workflow.GateRef) bool {
	for _, have := range stage.EffectiveGateGroups().Refs() {
		if have.Gate == ref.Gate && have.Version == ref.Version && have.InstanceName() == ref.InstanceName() &&
			have.Mode.Effective() == ref.Mode.Effective() && sameParams(have.Params, ref.Params) {
			return true
		}
	}
	return false
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
