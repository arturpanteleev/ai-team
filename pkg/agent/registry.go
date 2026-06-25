package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Registry struct {
	agentsDir string
	agents    map[string]*Agent
}

func NewRegistry(agentsDir string) *Registry {
	return &Registry{
		agentsDir: agentsDir,
		agents:    make(map[string]*Agent),
	}
}

func (r *Registry) Load(name string) (*Agent, error) {
	if a, ok := r.agents[name]; ok {
		return a, nil
	}

	defPath := filepath.Join(r.agentsDir, name, "def.yaml")
	data, err := os.ReadFile(defPath)
	if err != nil {
		return nil, fmt.Errorf("агент %s не найден: %w", name, err)
	}

	var a Agent
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("ошибка парсинга %s: %w", defPath, err)
	}

	promptPath := filepath.Join(r.agentsDir, name, a.PromptFile)
	if promptData, err := os.ReadFile(promptPath); err == nil {
		a.Prompt = string(promptData)
	}

	r.agents[name] = &a
	return &a, nil
}

func (r *Registry) List() []*Agent {
	var result []*Agent
	entries, err := os.ReadDir(r.agentsDir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if entry.IsDir() {
			a, err := r.Load(entry.Name())
			if err == nil {
				result = append(result, a)
			}
		}
	}
	return result
}

func (r *Registry) DefaultPipeline() []string {
	return []string{"analyst", "architect", "coder", "reviewer", "tester", "deployer"}
}
