package workflow

import "fmt"

const (
	TerminalComplete = "$complete"
	TerminalFailed   = "$failed"
	TerminalBlocked  = "$blocked"
	TerminalStop     = "$stop"
)

type ApprovalPolicy struct {
	Roles   []string          `json:"roles"`
	Quorum  string            `json:"quorum"`
	Actions map[string]string `json:"actions"`
	// Deferred — подтверждение перехода откладывается до delivery-решения
	// run'а (одно consolidated решение на весь delivery-план, APF-1). Пока
	// delivery не разрешён, deferred approval остаётся pending и attestуется
	// со своим точным subject; в strict/regulated профилях deferred=false.
	Deferred bool `json:"deferred,omitempty"`
}

type Node struct {
	Name      string `json:"name"`
	MaxVisits int    `json:"max_visits,omitempty"`
}

type Edge struct {
	From     string          `json:"from"`
	Outcome  Outcome         `json:"outcome"`
	To       string          `json:"to"`
	Approval *ApprovalPolicy `json:"approval,omitempty"`
}

type Graph struct {
	SchemaVersion int    `json:"schema_version"`
	Entry         string `json:"entry"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

func IsTerminal(target string) bool {
	switch target {
	case TerminalComplete, TerminalFailed, TerminalBlocked, TerminalStop:
		return true
	default:
		return false
	}
}

func (g Graph) Edge(from string, outcome Outcome) (Edge, bool) {
	for _, edge := range g.Edges {
		if edge.From == from && edge.Outcome == outcome {
			return edge, true
		}
	}
	return Edge{}, false
}

func (g Graph) Node(name string) (Node, bool) {
	for _, node := range g.Nodes {
		if node.Name == name {
			return node, true
		}
	}
	return Node{}, false
}

func (g Graph) Index(name string) int {
	for index, node := range g.Nodes {
		if node.Name == name {
			return index
		}
	}
	return -1
}

func (g Graph) Validate(requireApprovals, requireCycleLimits bool) error {
	if len(g.Nodes) == 0 {
		return fmt.Errorf("workflow graph: nodes пуст")
	}
	nodes := make(map[string]Node, len(g.Nodes))
	for _, node := range g.Nodes {
		if node.Name == "" {
			return fmt.Errorf("workflow graph: имя node обязательно")
		}
		if _, exists := nodes[node.Name]; exists {
			return fmt.Errorf("workflow graph: node %q повторяется", node.Name)
		}
		if node.MaxVisits < 0 {
			return fmt.Errorf("workflow graph: max_visits %s не может быть отрицательным", node.Name)
		}
		nodes[node.Name] = node
	}
	if _, exists := nodes[g.Entry]; !exists {
		return fmt.Errorf("workflow graph: entry %q не существует", g.Entry)
	}
	allowedOutcomes := map[Outcome]bool{
		OutcomePassed: true, OutcomeFailed: true, OutcomeRejected: true,
		OutcomeBlocked: true, OutcomeCanceled: true, OutcomeSkipped: true,
		OutcomeWarning: true,
	}
	seenEdges := make(map[string]bool)
	adjacency := make(map[string][]string)
	for _, edge := range g.Edges {
		if _, exists := nodes[edge.From]; !exists {
			return fmt.Errorf("workflow graph: edge from %q не существует", edge.From)
		}
		if !allowedOutcomes[edge.Outcome] {
			return fmt.Errorf("workflow graph: outcome %q недопустим", edge.Outcome)
		}
		if _, exists := nodes[edge.To]; !exists && !IsTerminal(edge.To) {
			return fmt.Errorf("workflow graph: edge target %q не существует", edge.To)
		}
		key := edge.From + "\x00" + string(edge.Outcome)
		if seenEdges[key] {
			return fmt.Errorf("workflow graph: неоднозначное ребро %s/%s", edge.From, edge.Outcome)
		}
		seenEdges[key] = true
		if !IsTerminal(edge.To) {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		}
		if requireApprovals && !IsTerminal(edge.To) && edge.Approval == nil {
			return fmt.Errorf("workflow graph: non-terminal edge %s → %s требует approval", edge.From, edge.To)
		}
		if edge.Approval != nil {
			if err := validateApproval(*edge.Approval, nodes); err != nil {
				return fmt.Errorf("workflow graph: edge %s/%s: %w", edge.From, edge.Outcome, err)
			}
			hasPrimaryTarget := false
			for _, target := range edge.Approval.Actions {
				hasPrimaryTarget = hasPrimaryTarget || target == edge.To
				if !IsTerminal(target) {
					adjacency[edge.From] = append(adjacency[edge.From], target)
				}
			}
			if !hasPrimaryTarget {
				return fmt.Errorf("workflow graph: edge %s/%s не имеет action к основному target %s", edge.From, edge.Outcome, edge.To)
			}
		}
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		for _, next := range adjacency[name] {
			visit(next)
		}
	}
	visit(g.Entry)
	for name := range nodes {
		if !reachable[name] {
			return fmt.Errorf("workflow graph: node %q недостижим из entry", name)
		}
	}
	if !hasTerminalPath(g.Entry, g.Edges, map[string]bool{}) {
		return fmt.Errorf("workflow graph: из entry нет пути к terminal target")
	}
	if cycle := cycleNodes(adjacency); requireCycleLimits && len(cycle) > 0 {
		for name := range cycle {
			if nodes[name].MaxVisits <= 0 {
				return fmt.Errorf("workflow graph: node %q входит в цикл и требует max_visits", name)
			}
		}
	}
	return nil
}

func validateApproval(policy ApprovalPolicy, nodes map[string]Node) error {
	if len(policy.Roles) == 0 {
		return fmt.Errorf("approval roles пуст")
	}
	roles := map[string]bool{}
	for _, role := range policy.Roles {
		if role == "" || roles[role] {
			return fmt.Errorf("approval roles содержат пустое или повторяющееся значение")
		}
		roles[role] = true
	}
	if policy.Quorum != "any" && policy.Quorum != "all" {
		return fmt.Errorf("approval quorum должен быть any или all")
	}
	if len(policy.Actions) == 0 {
		return fmt.Errorf("approval actions пуст")
	}
	for action, target := range policy.Actions {
		if action == "" {
			return fmt.Errorf("approval action пуст")
		}
		if _, exists := nodes[target]; !exists && !IsTerminal(target) {
			return fmt.Errorf("action %s target %q не существует", action, target)
		}
	}
	return nil
}

func hasTerminalPath(node string, edges []Edge, visiting map[string]bool) bool {
	if visiting[node] {
		return false
	}
	visiting[node] = true
	defer delete(visiting, node)
	for _, edge := range edges {
		if edge.From != node {
			continue
		}
		if IsTerminal(edge.To) || hasTerminalPath(edge.To, edges, visiting) {
			return true
		}
		for _, target := range approvalTargets(edge.Approval) {
			if IsTerminal(target) || hasTerminalPath(target, edges, visiting) {
				return true
			}
		}
	}
	return false
}

func approvalTargets(policy *ApprovalPolicy) []string {
	if policy == nil {
		return nil
	}
	targets := make([]string, 0, len(policy.Actions))
	for _, target := range policy.Actions {
		targets = append(targets, target)
	}
	return targets
}

func cycleNodes(adjacency map[string][]string) map[string]bool {
	result, stack, active, done := map[string]bool{}, []string{}, map[string]int{}, map[string]bool{}
	var walk func(string)
	walk = func(node string) {
		if done[node] {
			return
		}
		if start, exists := active[node]; exists {
			for _, item := range stack[start:] {
				result[item] = true
			}
			return
		}
		active[node] = len(stack)
		stack = append(stack, node)
		for _, next := range adjacency[node] {
			walk(next)
		}
		stack = stack[:len(stack)-1]
		delete(active, node)
		done[node] = true
	}
	for node := range adjacency {
		walk(node)
	}
	return result
}
