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
	var problems []string
	registry := gatelib.Default()
	for _, stage := range c.Stages {
		for _, ref := range stage.EffectiveGateGroups().Refs() {
			if isCommandGate(registry, ref) {
				problems = append(problems, fmt.Sprintf("этап %q: гейт %s запускает команду или контейнер", stage.ID, ref.Gate))
			}
		}
	}
	if trusted == nil {
		trusted = &Config{}
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
	problems = append(problems, c.infrastructureProblems(trusted)...)
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
)

// configFieldClasses is the allow-list of Config fields the web may change. A
// field absent from it fails TestConfigFieldClassification, so a new section
// cannot reach the web unclassified; at run time an unclassified field is
// compared like a trusted one.
var configFieldClasses = map[string]fieldClass{
	"QuestionStages": derivedField, "PipelineAgents": derivedField, "Workflow": derivedField,
	"SchemaVersion": processField, "Template": processField, "Title": processField,
	"StallAfter": processField, "ForbidSelfApproval": processField,
	"Stages": partialField, "Returns": processField, "MaxVisits": processField,
	"Checks": definitionsField, "MCPServers": definitionsField, "ExecutorAdapters": definitionsField,
	"CLI": trustedField, "Model": trustedField, "Effort": trustedField,
	"StageTimeout": trustedField, "GateConcurrency": trustedField,
	"PreflightTimeout": trustedField, "DeliveryTimeout": trustedField,
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
	"ID": processField, "Title": processField, "Function": processField,
	"ApproverFunction": processField, "Result": processField, "Executor": processField,
	"Agent": processField, "Adapter": processField, "LinkKind": processField,
	"RequiredSections": processField, "Check": processField, "Confirm": processField,
	"ExitGateGroups": processField, "Skippable": processField, "MCPServers": processField,
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

func (c *Config) infrastructureProblems(trusted *Config) []string {
	var problems []string
	mine, theirs := reflect.ValueOf(c).Elem(), reflect.ValueOf(trusted).Elem()
	typ := mine.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		switch configFieldClasses[field.Name] {
		case processField, derivedField, definitionsField, partialField:
		default:
			// trustedField, and any field nobody classified: protect by default.
			problems = append(problems, diffInfrastructure(yamlName(field), mine.Field(i), theirs.Field(i))...)
		}
	}
	problems = append(problems, trackerProblems(c.Tracker, trusted.Tracker)...)
	problems = append(problems, c.deliveryProblems(trusted)...)
	return problems
}

func trackerProblems(submitted, trusted *TrackerConfig) []string {
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
	if aEmpty != bEmpty || a.Kind() != reflect.Struct || a.Type() != b.Type() {
		if !aEmpty && !bEmpty && reflect.DeepEqual(a.Interface(), b.Interface()) {
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

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	}
	return v.IsZero() && v.Kind() != reflect.Struct
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
