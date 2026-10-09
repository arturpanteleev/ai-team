package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

var templateIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// TemplateStage is one ordered node in a project's process template.
type TemplateStage struct {
	ID               string            `yaml:"id"`
	Title            string            `yaml:"title"`
	Function         string            `yaml:"function"`
	Result           string            `yaml:"result"`
	Executor         string            `yaml:"executor,omitempty"`
	Agent            string            `yaml:"agent,omitempty"`
	LinkKind         string            `yaml:"link_kind,omitempty"`
	RequiredSections []string          `yaml:"required_sections,omitempty"`
	Check            *TemplateCheck    `yaml:"check,omitempty"`
	Confirm          string            `yaml:"confirm,omitempty"`
	Skippable        bool              `yaml:"skippable,omitempty"`
	Delivery         *TemplateDelivery `yaml:"delivery,omitempty"`
	MCPServers       []string          `yaml:"mcp_servers,omitempty"`
}

// TemplateCheck keeps the result check declarative. Execution is introduced by
// the later stage-check capability; B-20 validates and snapshots its schema.
type TemplateCheck struct {
	Kind      string   `yaml:"kind"`
	Agent     string   `yaml:"agent,omitempty"`
	Mode      string   `yaml:"mode,omitempty"`
	MaxRounds int      `yaml:"max_rounds,omitempty"`
	Rules     []string `yaml:"rules,omitempty"`
}

// TemplateDelivery declares controller checks and optional earlier verdict
// stages required before controller-owned delivery for an agent-executed PR stage.
type TemplateDelivery struct {
	RequireChecks   []string `yaml:"require_checks"`
	RequireVerdicts []string `yaml:"require_verdicts,omitempty"`
}

// TemplateReturn declares a backward-only route. MaxVisits is optional; when
// omitted the per-target default (or Config.MaxVisits entry) is used.
type TemplateReturn struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	MaxVisits int    `yaml:"max_visits,omitempty"`
}

func (s *TemplateStage) UnmarshalYAML(node *yaml.Node) error {
	if err := validateMappingKeys(node, map[string]bool{
		"id": true, "title": true, "function": true, "result": true,
		"executor": true, "agent": true, "link_kind": true,
		"required_sections": true, "check": true, "confirm": true,
		"skippable": true, "delivery": true, "mcp_servers": true,
	}, "config: stage"); err != nil {
		return err
	}
	type plain TemplateStage
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = TemplateStage(decoded)
	if s.Executor == "" {
		s.Executor = "human"
	}
	if s.Confirm == "" {
		s.Confirm = defaultConfirmForResult(s.Result)
	}
	return nil
}

// defaultConfirmForResult keeps YAML-loaded and programmatically constructed
// templates consistent when a caller omits the optional confirmation mode.
func defaultConfirmForResult(result string) string {
	if result == "approve" {
		return "auto"
	}
	return "required"
}

func (c *TemplateCheck) UnmarshalYAML(node *yaml.Node) error {
	if err := validateMappingKeys(node, map[string]bool{
		"kind": true, "agent": true, "mode": true, "max_rounds": true, "rules": true,
	}, "config: stage check"); err != nil {
		return err
	}
	type plain TemplateCheck
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = TemplateCheck(decoded)
	return nil
}

func (d *TemplateDelivery) UnmarshalYAML(node *yaml.Node) error {
	if err := validateMappingKeys(node, map[string]bool{"require_checks": true, "require_verdicts": true}, "config: stage delivery"); err != nil {
		return err
	}
	type plain TemplateDelivery
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*d = TemplateDelivery(decoded)
	return nil
}

func (r *TemplateReturn) UnmarshalYAML(node *yaml.Node) error {
	if err := validateMappingKeys(node, map[string]bool{"from": true, "to": true, "max_visits": true}, "config: return"); err != nil {
		return err
	}
	type plain TemplateReturn
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*r = TemplateReturn(decoded)
	return nil
}

func (c *Config) validateTemplate(reg AgentLookup) error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if !templateIDPattern.MatchString(c.Template) {
		add("template: невалидный id %q", c.Template)
	}
	if c.Title == "" || strings.TrimSpace(c.Title) != c.Title {
		add("title: обязателен и не должен содержать пробелы по краям")
	}
	if c.StallAfter != "" {
		if duration, err := time.ParseDuration(c.StallAfter); err != nil || duration <= 0 {
			add("stall_after %q не парсится (пример: 24h)", c.StallAfter)
		}
	}
	if len(c.Stages) == 0 {
		add("stages: должен содержать хотя бы один этап")
	}
	index := make(map[string]int, len(c.Stages))
	agentNames := make(map[string]bool)
	for i, stage := range c.Stages {
		prefix := fmt.Sprintf("stages[%d] (%s)", i, stage.ID)
		if !templateIDPattern.MatchString(stage.ID) {
			add("%s: id обязателен и должен быть корректным идентификатором", prefix)
		}
		if _, exists := index[stage.ID]; exists {
			add("%s: id повторяется", prefix)
		}
		index[stage.ID] = i
		if stage.Title == "" || strings.TrimSpace(stage.Title) != stage.Title {
			add("%s: title обязателен", prefix)
		}
		if stage.Function == "" || strings.TrimSpace(stage.Function) != stage.Function {
			add("%s: function обязателен", prefix)
		}
		if stage.Result != "md" && stage.Result != "link" && stage.Result != "approve" {
			add("%s: result %q недопустим (md|link|approve)", prefix, stage.Result)
		}
		if stage.Executor != "human" && stage.Executor != "agent" {
			add("%s: executor %q недопустим (human|agent)", prefix, stage.Executor)
		}
		if stage.Executor == "agent" && stage.Agent == "" {
			add("%s: executor agent требует agent", prefix)
		}
		if stage.Agent != "" {
			if !templateIDPattern.MatchString(stage.Agent) {
				add("%s: невалидный agent %q", prefix, stage.Agent)
			}
			agentNames[stage.Agent] = true
			if reg != nil && !reg.Exists(stage.Agent) {
				add("%s: агент %q не найден в registry", prefix, stage.Agent)
			}
		}
		if len(stage.MCPServers) > 0 {
			if stage.Agent == "" {
				add("%s: mcp_servers допустимы только для stage с agent", prefix)
			}
			if c.CLI != "codex" {
				add("%s: mcp_servers поддерживаются только при cli: codex", prefix)
			}
			if len(stage.MCPServers) > runtime.MaxMCPServersPerStage {
				add("%s: допускается не более %d mcp_servers", prefix, runtime.MaxMCPServersPerStage)
			}
			seenServers := map[string]bool{}
			for _, serverID := range stage.MCPServers {
				if strings.TrimSpace(serverID) == "" || seenServers[serverID] {
					add("%s: mcp_servers содержит пустой или повторяющийся id", prefix)
				}
				seenServers[serverID] = true
				if _, exists := c.MCPServers[serverID]; !exists {
					add("%s: mcp_servers ссылается на неизвестный сервер %q", prefix, serverID)
				}
			}
		}
		if stage.Result == "link" {
			if stage.LinkKind != "pr" && stage.LinkKind != "build" && stage.LinkKind != "other" {
				add("%s: link_kind %q недопустим (pr|build|other)", prefix, stage.LinkKind)
			}
		} else if stage.LinkKind != "" {
			add("%s: link_kind допустим только для result link", prefix)
		}
		if len(stage.RequiredSections) > 0 && stage.Result != "md" {
			add("%s: required_sections допустим только для result md", prefix)
		}
		seenSections := map[string]bool{}
		for _, section := range stage.RequiredSections {
			if strings.TrimSpace(section) == "" || seenSections[section] {
				add("%s: required_sections содержит пустое или повторяющееся значение", prefix)
			}
			seenSections[section] = true
		}
		if stage.Confirm != "" && stage.Confirm != "required" && stage.Confirm != "auto" {
			add("%s: confirm %q недопустим (required|auto)", prefix, stage.Confirm)
		}
		if stage.Check != nil {
			check := stage.Check
			switch check.Kind {
			case "hard":
				if len(check.Rules) == 0 {
					add("%s: check kind hard требует rules", prefix)
				}
				if check.Agent != "" || check.Mode != "" || check.MaxRounds != 0 {
					add("%s: check agent/mode/max_rounds допустимы только для kind agent", prefix)
				}
			case "agent":
				if check.Agent == "" {
					add("%s: check kind agent требует agent", prefix)
				} else {
					agentNames[check.Agent] = true
					if reg != nil && !reg.Exists(check.Agent) {
						add("%s: агент проверки %q не найден в registry", prefix, check.Agent)
					}
				}
				if check.MaxRounds < 0 {
					add("%s: check max_rounds не может быть отрицательным", prefix)
				}
			default:
				add("%s: check kind %q недопустим (hard|agent)", prefix, check.Kind)
			}
		}
		if stage.Delivery != nil {
			if stage.Result != "link" || stage.LinkKind != "pr" || stage.Agent == "" {
				add("%s: delivery допустим только для PR-этапа с agent", prefix)
			}
			seen := map[string]bool{}
			for _, checkName := range stage.Delivery.RequireChecks {
				if checkName == "" || seen[checkName] {
					add("%s: delivery.require_checks содержит пустое или повторяющееся имя", prefix)
				}
				seen[checkName] = true
			}
			seenVerdicts := map[string]bool{}
			for _, verdictStage := range stage.Delivery.RequireVerdicts {
				if verdictStage == "" || seenVerdicts[verdictStage] {
					add("%s: delivery.require_verdicts содержит пустой или повторяющийся этап", prefix)
				}
				seenVerdicts[verdictStage] = true
			}
		}
	}
	projectCheckNames := make(map[string]bool, len(c.Checks))
	projectChecks := make(map[string]checks.Definition, len(c.Checks))
	for _, check := range c.Checks {
		if err := check.Validate(); err != nil {
			add("checks: %v", err)
		}
		if projectCheckNames[check.Name] {
			add("checks: имя %q повторяется", check.Name)
		}
		projectCheckNames[check.Name] = true
		projectChecks[check.Name] = check
	}
	for i, stage := range c.Stages {
		if stage.Delivery == nil {
			continue
		}
		for _, requiredCheck := range stage.Delivery.RequireChecks {
			check, exists := projectChecks[requiredCheck]
			if !exists {
				add("stages[%d] (%s): delivery.require_checks ссылается на неизвестную проверку %q", i, stage.ID, requiredCheck)
			} else if check.Policy != checks.PolicyRequired {
				add("stages[%d] (%s): delivery.require_checks проверка %q должна иметь policy required", i, stage.ID, requiredCheck)
			}
		}
	}
	for i, route := range c.Returns {
		from, fromExists := index[route.From]
		to, toExists := index[route.To]
		if !fromExists || !toExists {
			add("returns[%d]: from/to должны ссылаться на существующие stages", i)
			continue
		}
		if to >= from {
			add("returns[%d]: возврат %s → %s должен вести только назад", i, route.From, route.To)
		}
		if route.MaxVisits < 0 {
			add("returns[%d]: max_visits не может быть отрицательным", i)
		}
	}
	for i, stage := range c.Stages {
		if stage.Delivery == nil {
			continue
		}
		for _, verdictStage := range stage.Delivery.RequireVerdicts {
			verdictIndex, exists := index[verdictStage]
			if !exists {
				add("stages[%d] (%s): delivery.require_verdicts ссылается на неизвестный этап %q", i, stage.ID, verdictStage)
			} else if verdictStage == stage.ID {
				add("stages[%d] (%s): delivery.require_verdicts не может ссылаться на сам delivery-этап", i, stage.ID)
			} else if verdictIndex >= i {
				add("stages[%d] (%s): delivery.require_verdicts должен ссылаться на этап до delivery, получен %q", i, stage.ID, verdictStage)
			} else if c.Stages[verdictIndex].Agent == "" {
				add("stages[%d] (%s): required verdict stage %q должен иметь agent с verdict contract", i, stage.ID, verdictStage)
			} else if reg == nil {
				add("stages[%d] (%s): проверка delivery.require_verdicts для %q требует registry с verdict contract", i, stage.ID, verdictStage)
			} else if lookup, ok := reg.(requiredVerdictContractLookup); !ok {
				add("stages[%d] (%s): registry не поддерживает проверку required verdict contract для %q", i, stage.ID, verdictStage)
			} else {
				hasContract, err := lookup.HasRequiredVerdictContract(c.Stages[verdictIndex].Agent)
				if err != nil {
					add("stages[%d] (%s): проверить verdict contract агента %q для этапа %q: %v", i, stage.ID, c.Stages[verdictIndex].Agent, verdictStage, err)
				} else if !hasContract {
					add("stages[%d] (%s): required verdict stage %q должен иметь verdict.required contract в registry definition агента %q", i, stage.ID, verdictStage, c.Stages[verdictIndex].Agent)
				}
			}
		}
	}
	returnMax := make(map[string]int)
	returnTargets := make(map[string]bool)
	seenRoutes := make(map[string]bool)
	for _, route := range c.Returns {
		returnTargets[route.To] = true
		key := route.From + "\x00" + route.To
		if seenRoutes[key] {
			add("returns: маршрут %s → %s повторяется", route.From, route.To)
		}
		seenRoutes[key] = true
		if route.MaxVisits <= 0 {
			continue
		}
		if previous := returnMax[route.To]; previous != 0 && previous != route.MaxVisits {
			add("returns: маршруты к %s задают разные max_visits", route.To)
		} else {
			returnMax[route.To] = route.MaxVisits
		}
	}
	for id, visits := range c.MaxVisits {
		if visits <= 0 {
			add("max_visits[%s] должен быть положительным", id)
		}
		if !indexContains(index, id) {
			add("max_visits ссылается на неизвестный stage %q", id)
		}
		if !returnTargets[id] {
			add("max_visits[%s] допустим только для stage, в который ведёт возврат", id)
		}
		if previous := returnMax[id]; previous != 0 && previous != visits {
			add("max_visits[%s] не совпадает с лимитом возврата", id)
		}
	}
	for _, route := range c.Returns {
		if route.MaxVisits > 0 {
			if visits := c.MaxVisits[route.To]; visits != 0 && visits != route.MaxVisits {
				add("max_visits[%s] не совпадает с лимитом возврата", route.To)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("невалидный config.yaml:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func indexContains(index map[string]int, key string) bool { _, ok := index[key]; return ok }

// TemplateGraph compiles the ordered stages and backward return routes into a
// graph keyed by stable stage IDs. Forward edges are always added in stage
// order; return actions are grouped by source so several backward choices do
// not create ambiguous outcome edges.
func (c *Config) TemplateGraph() (workflow.Graph, error) {
	graph := workflow.Graph{SchemaVersion: CurrentSchemaVersion}
	if len(c.Stages) == 0 {
		return graph, fmt.Errorf("workflow graph: stages пуст")
	}
	graph.Entry = c.Stages[0].ID
	for _, stage := range c.Stages {
		visits := 0
		if c.MaxVisits != nil {
			visits = c.MaxVisits[stage.ID]
		}
		if visits == 0 {
			for _, route := range c.Returns {
				if route.To == stage.ID {
					visits = route.MaxVisits
					if visits == 0 {
						visits = 3
					}
					break
				}
			}
		}
		graph.Nodes = append(graph.Nodes, workflow.Node{ID: stage.ID, Agent: stage.Agent, MaxVisits: visits})
	}
	for i, stage := range c.Stages {
		next := workflow.TerminalComplete
		if i+1 < len(c.Stages) {
			next = c.Stages[i+1].ID
		}
		edge := workflow.Edge{From: stage.ID, Outcome: workflow.OutcomePassed, To: next}
		confirm := stage.Confirm
		if confirm == "" {
			confirm = defaultConfirmForResult(stage.Result)
		}
		if next != workflow.TerminalComplete && confirm == "required" {
			edge.Approval = generatedApproval(stage.Function, next, nil)
		}
		graph.Edges = append(graph.Edges, edge)
	}
	bySource := make(map[string][]string)
	for _, route := range c.Returns {
		bySource[route.From] = append(bySource[route.From], route.To)
	}
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		targets := bySource[source]
		sort.SliceStable(targets, func(i, j int) bool { return targets[i] < targets[j] })
		actions := make(map[string]string, len(targets)+1)
		for _, target := range targets {
			actions["return_to_"+target] = target
		}
		actions["reject"] = workflow.TerminalStop
		graph.Edges = append(graph.Edges, workflow.Edge{
			From: source, Outcome: workflow.OutcomeRejected, To: targets[0],
			Approval: generatedApproval(stageFunction(c.Stages, source), targets[0], actions),
		})
	}
	// Schema v5 controls confirmation per stage. Forward transitions with
	// confirm:auto are valid without an edge approval; required stages compile
	// one above, and backward return transitions always retain their approval.
	if err := graph.Validate(false, false); err != nil {
		return graph, err
	}
	return graph, nil
}

func generatedApproval(function, primary string, actions map[string]string) *workflow.ApprovalPolicy {
	if actions == nil {
		actions = map[string]string{"approve": primary, "reject": workflow.TerminalStop}
	}
	role := cloudRoleForFunction(function)
	if role == "" {
		role = "operator"
	}
	return &workflow.ApprovalPolicy{Roles: []string{role}, Quorum: "any", Actions: actions}
}

func cloudRoleForFunction(function string) string {
	if role, ok := cloudidentity.FunctionRole(function); ok {
		return string(role)
	}
	return function
}

func stageFunction(stages []TemplateStage, id string) string {
	for _, stage := range stages {
		if stage.ID == id {
			return stage.Function
		}
	}
	return "operator"
}

func (c *Config) templateAgentNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, stage := range c.Stages {
		for _, name := range []string{stage.Agent} {
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		if stage.Check != nil && stage.Check.Agent != "" && !seen[stage.Check.Agent] {
			seen[stage.Check.Agent] = true
			names = append(names, stage.Check.Agent)
		}
	}
	sort.Strings(names)
	return names
}
