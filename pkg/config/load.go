package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("config: multiple YAML documents are not supported")
		}
		return nil, err
	}
	return &cfg, nil
}

// defaultLoopbackSources — стадии, чей негативный вердикт возвращается к coder.
var defaultLoopbackSources = []string{"reviewer", "tester", "verifier"}

// defaultStageRoles — human-роли рёбер default workflow стандартного профиля.
var defaultStageRoles = map[string][]string{
	"analyst":   {"product_owner"},
	"architect": {"architect"},
	"coder":     {"developer"},
	"reviewer":  {"reviewer"},
	"tester":    {"qa"},
	"verifier":  {"qa"},
}

// Profile — уровень строгости workflow, разворачиваемый init'ом в готовый
// конфиг v4. Профиль — свойство генерации конфига, не runtime-сущность:
// итоговый config.yaml остаётся обычным явным графом.
const (
	ProfileStandard  = "standard"
	ProfileFast      = "fast"
	ProfileRegulated = "regulated"
)

type stageSpec struct {
	name      string
	roles     []string
	maxVisits int
}

// DefaultProfile строит готовый конфиг v4 для профиля:
//
//   - standard: полный конвейер из 7 стадий, quorum any;
//   - fast: без verifier, reviewer совмещает ревью и верификацию
//     (project-local override пишет cmd/init), меньше max_visits;
//   - regulated: полный конвейер, quorum all на смысловых рёбрах,
//     max_visits снижен.
func DefaultProfile(profile string) (*Config, error) {
	var stages []stageSpec
	quorum := QuorumAny()
	loopbackQuorum := QuorumAny()
	maxVisits := 3
	switch profile {
	case ProfileStandard:
		stages = []stageSpec{
			{name: "analyst", roles: []string{"product_owner"}},
			{name: "architect", roles: []string{"architect"}},
			{name: "coder", roles: []string{"developer"}, maxVisits: 3},
			{name: "reviewer", roles: []string{"reviewer"}, maxVisits: 3},
			{name: "tester", roles: []string{"qa"}, maxVisits: 3},
			{name: "verifier", roles: []string{"qa"}, maxVisits: 3},
			{name: "deployer"},
		}
	case ProfileFast:
		stages = []stageSpec{
			{name: "analyst", roles: []string{"product_owner"}},
			{name: "coder", roles: []string{"developer"}, maxVisits: 2},
			{name: "tester", roles: []string{"qa"}, maxVisits: 2},
			{name: "reviewer", roles: []string{"reviewer"}, maxVisits: 2},
			{name: "deployer"},
		}
	case ProfileRegulated:
		quorum = QuorumAll()
		loopbackQuorum = QuorumAll()
		maxVisits = 2
		stages = []stageSpec{
			{name: "analyst", roles: []string{"product_owner"}},
			{name: "architect", roles: []string{"architect"}},
			{name: "coder", roles: []string{"developer"}, maxVisits: 2},
			{name: "reviewer", roles: []string{"reviewer"}, maxVisits: 2},
			{name: "tester", roles: []string{"qa"}, maxVisits: 2},
			{name: "verifier", roles: []string{"qa"}, maxVisits: 2},
			{name: "deployer"},
		}
	default:
		return nil, fmt.Errorf("неизвестный профиль %q (допустимы %s, %s, %s)",
			profile, ProfileFast, ProfileStandard, ProfileRegulated)
	}
	return buildConfig(profile, stages, quorum, loopbackQuorum, maxVisits), nil
}

func QuorumAny() string { return "any" }
func QuorumAll() string { return "all" }

func buildConfig(profile string, stages []stageSpec, quorum, loopbackQuorum string, maxVisits int) *Config {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.name
	}
	agents := make([]AgentConfig, len(names))
	for i, name := range names {
		agents[i] = AgentConfig{Name: name}
	}
	cfg := &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: agents,
		CLI:            "opencode",
		Effort:         "medium",
		StageTimeout:   "30m",
	}
	cfg.Workflow = buildWorkflow(profile, names, stages, quorum, loopbackQuorum, maxVisits)
	return cfg
}

func buildWorkflow(profile string, names []string, stages []stageSpec, quorum, loopbackQuorum string, defaultMaxVisits int) *WorkflowConfig {
	index := make(map[string]int, len(names))
	for i, name := range names {
		index[name] = i
	}
	specByName := make(map[string]stageSpec, len(stages))
	for _, s := range stages {
		specByName[s.name] = s
	}
	workflowConfig := &WorkflowConfig{Entry: names[0], MaxVisits: map[string]int{}}
	// APF-1: в standard/fast профилях forward gate-переходы откладываются и
	// подтверждаются одним consolidated delivery-решением (1–2 клика на фичу);
	// regulated сохраняет пошаговые approvals (quorum all).
	deferredGates := profile == ProfileStandard || profile == ProfileFast
	for i, name := range names {
		target := "$complete"
		if i+1 < len(names) {
			target = names[i+1]
		}
		edge := WorkflowEdgeConfig{From: name, Outcome: "passed", To: target}
		if target != "$complete" {
			roles := append([]string(nil), specByName[name].roles...)
			if len(roles) == 0 {
				roles = []string{"operator"}
			}
			edge.Approval = &WorkflowApprovalConfig{
				Roles: roles, Quorum: quorum, Deferred: deferredGates,
				Actions: map[string]string{"approve": target, "reject": "$stop"},
			}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, edge)
	}
	coderIdx, hasCoder := index["coder"]
	for _, source := range defaultLoopbackSources {
		srcIdx, srcExists := index[source]
		if !hasCoder || !srcExists || srcIdx <= coderIdx {
			continue
		}
		override := "$complete"
		if srcIdx+1 < len(names) {
			override = names[srcIdx+1]
		}
		roles := append([]string(nil), specByName[source].roles...)
		if len(roles) == 0 {
			roles = []string{"reviewer"}
		}
		workflowConfig.Edges = append(workflowConfig.Edges, WorkflowEdgeConfig{
			From: source, Outcome: "rejected", To: "coder",
			Approval: &WorkflowApprovalConfig{
				Roles: roles, Quorum: loopbackQuorum,
				Actions: map[string]string{
					"return_to_coder": "coder", "override_approve": override, "reject": "$stop",
				},
			},
		})
	}
	for _, name := range names {
		if mv := specByName[name].maxVisits; mv > 0 {
			workflowConfig.MaxVisits[name] = mv
		} else if name != "deployer" && name != "analyst" && name != "architect" {
			workflowConfig.MaxVisits[name] = defaultMaxVisits
		}
	}
	return workflowConfig
}

// Default — стандартный профиль (совместимость со старыми вызовами).
func Default() *Config {
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		panic(err)
	}
	return cfg
}

// Marshal сериализует конфиг в YAML (используется init-ом: граф и
// approval-политики сохраняются целиком).
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}
