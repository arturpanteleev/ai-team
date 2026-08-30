package pipeline

import (
	"fmt"
	"sort"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

// captureProvenance строит authority-bearing provenance manifest v1 (V0-2) из
// текущего resolved состояния пайплайна. Каждое неперечислимое deterministic
// значение помечается unknown — никаких догадок о модели или usage.
// Base/candidate identity фиксируется по стабильному control-состоянию
// (base commit/tree), а не по mutable workspace-хэшу: содержимое worktree
// меняется между попытками и отдельно охраняется approval'ами с exact
// CandidateSHA256.
func (p *Pipeline) captureProvenance(runID string, identity evidence.ControllerIdentity, candidateManager *candidate.Manager) (*provenance.Manifest, error) {
	manifest := provenance.New(runID, time.Now().UTC())
	manifest.AddJSON(provenance.KindRuntime, "", identity)

	type providerModel struct {
		CLI    string `json:"cli,omitempty"`
		Model  string `json:"model,omitempty"`
		Effort string `json:"effort,omitempty"`
	}
	configSurface := struct {
		Globals providerModel            `json:"globals"`
		Stages  map[string]providerModel `json:"stages"`
	}{
		Globals: providerModel{CLI: p.cfg.CLI, Model: p.cfg.Model, Effort: p.cfg.Effort},
		Stages:  make(map[string]providerModel),
	}
	for _, name := range p.cfg.AgentNames() {
		ac := p.cfg.AgentConfig(name)
		if ac == nil {
			continue
		}
		configSurface.Stages[name] = providerModel{CLI: ac.CLI, Model: ac.Model, Effort: ac.Effort}
	}
	manifest.AddJSON(provenance.KindConfig, "", configSurface)

	for _, name := range p.cfg.AgentNames() {
		definition, err := p.reg.Load(name)
		if err != nil {
			return nil, fmt.Errorf("provenance definition %s: %w", name, err)
		}
		projection := agentDefinitionProjection{
			Name:             definition.Name,
			RuntimeType:      definition.RuntimeType,
			CLI:              definition.CLI,
			Kind:             definition.Kind,
			Mutation:         definition.Mutation,
			AllowedPaths:     append([]string(nil), definition.AllowedPaths...),
			RequireDiff:      definition.RequireDiff,
			TestModifyPolicy: definition.TestModifyPolicy,
			Inputs:           definition.Inputs,
			Outputs:          definition.Outputs,
			AskQuestions:     definition.AskQuestions,
			Checks:           definition.Checks,
			Verdict:          definition.Verdict,
			Preconditions:    definition.Preconditions,
		}
		manifest.AddJSON(provenance.KindAgent, name, projection)

		if definition.Prompt != "" {
			manifest.AddBytes(provenance.KindPrompt, name, []byte(definition.Prompt))
		} else {
			manifest.AddUnknown(provenance.KindPrompt, name)
		}

		checkSuite := append([]checks.Definition(nil), definition.Checks...)
		sort.SliceStable(checkSuite, func(a, b int) bool { return checkSuite[a].Name < checkSuite[b].Name })
		if checkSuite == nil {
			checkSuite = []checks.Definition{}
		}
		manifest.AddJSON(provenance.KindCheckSuite, name, checkSuite)

		ac := p.cfg.AgentConfig(name)
		pm := providerModel{}
		if ac != nil {
			pm = providerModel{CLI: ac.CLI, Model: ac.Model, Effort: ac.Effort}
		}
		if pm.CLI == "" && pm.Model == "" && pm.Effort == "" {
			manifest.AddUnknown(provenance.KindProviderModel, name)
		} else {
			manifest.AddJSON(provenance.KindProviderModel, name, pm)
		}
	}

	if candidateManager != nil {
		meta := candidateManager.Metadata()
		base := struct {
			BaseCommit string `json:"base_commit"`
			BaseTree   string `json:"base_tree"`
		}{BaseCommit: meta.BaseCommit, BaseTree: meta.BaseTree}
		manifest.AddJSON(provenance.KindBase, "", base)
		manifest.AddJSON(provenance.KindCandidate, "", meta)
	} else {
		// Вне Git snapshot всего workspace хэшируется внутри guard — здесь
		// стабильной authority-identity нет, поэтому честно unknown.
		manifest.AddUnknown(provenance.KindBase, "")
		manifest.AddUnknown(provenance.KindCandidate, "")
	}
	return manifest, nil
}

// agentDefinitionProjection — authority-bearing срез definition агента для
// provenance. Prompt исключён (отдельный источник), описательные поля
// (description и т.п.) на поведение не влияют и не входят в контракт.
type agentDefinitionProjection struct {
	Name             string                       `json:"name"`
	RuntimeType      string                       `json:"runtime"`
	CLI              string                       `json:"cli,omitempty"`
	Kind             string                       `json:"kind,omitempty"`
	Mutation         string                       `json:"mutation,omitempty"`
	AllowedPaths     []string                     `json:"allowed_paths,omitempty"`
	RequireDiff      bool                         `json:"require_diff,omitempty"`
	TestModifyPolicy string                       `json:"test_modify_policy,omitempty"`
	Inputs           map[string]string            `json:"inputs,omitempty"`
	Outputs          map[string]string            `json:"outputs,omitempty"`
	AskQuestions     bool                         `json:"ask_questions,omitempty"`
	Checks           []checks.Definition          `json:"checks,omitempty"`
	Verdict          *verdict.Contract            `json:"verdict,omitempty"`
	Preconditions    map[string]*verdict.Contract `json:"preconditions,omitempty"`
}
