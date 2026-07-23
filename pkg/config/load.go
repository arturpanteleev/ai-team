package config

import (
	"bytes"
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
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
	if cfg.SchemaVersion == PreviousSchemaVersion {
		migrateV2ToV3(&cfg)
	}
	return &cfg, nil
}

func migrateV2ToV3(cfg *Config) {
	for agentIndex := range cfg.PipelineAgents {
		for checkIndex := range cfg.PipelineAgents[agentIndex].Checks {
			check := &cfg.PipelineAgents[agentIndex].Checks[checkIndex]
			if check.Adapter != "" || len(check.Command) < 2 || check.Command[0] != "go" || check.Command[1] != "test" {
				continue
			}
			check.Adapter = checks.AdapterGoTest
			if !hasArgument(check.Command[2:], "-json") {
				check.Command = append(check.Command[:2], append([]string{"-json"}, check.Command[2:]...)...)
			}
			if !hasArgument(check.Command[2:], "-count=1") {
				check.Command = append(check.Command[:2], append([]string{"-count=1"}, check.Command[2:]...)...)
			}
		}
	}
	cfg.SchemaVersion = CurrentSchemaVersion
}

func hasArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

// defaultAgentOverrides carries the per-stage config beyond a bare name for
// the default pipeline. The stage names/order themselves come from
// (*agent.Registry).DefaultPipeline() — the single source of truth openspec's
// verifier-integration spec already names — rather than being duplicated
// here independently.
var defaultAgentOverrides = map[string]AgentConfig{
	"analyst":   {CheckpointAfter: CheckpointRequireExplicit},
	"architect": {CheckpointAfter: CheckpointRequireExplicit},
	"coder":     {MaxRetries: 2},
}

func Default() *Config {
	names := (&agent.Registry{}).DefaultPipeline()
	agents := make([]AgentConfig, len(names))
	for i, name := range names {
		cfg := defaultAgentOverrides[name]
		cfg.Name = name
		agents[i] = cfg
	}
	return &Config{
		SchemaVersion:  CurrentSchemaVersion,
		PipelineAgents: agents,
		CLI:            "opencode",
		Effort:         "medium",
		StageTimeout:   "30m",
	}
}

// Marshal сериализует конфиг в YAML (используется init-ом: гейты и
// ролевые поля сохраняются, в отличие от ручной сборки строки).
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}
