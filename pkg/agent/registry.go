package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Registry struct {
	fsys   fs.FS
	agents map[string]*Agent
}

func NewRegistry(agentsDir string) *Registry {
	absDir, err := filepath.Abs(agentsDir)
	if err != nil {
		absDir = agentsDir
	}
	return &Registry{
		fsys:   os.DirFS(absDir),
		agents: make(map[string]*Agent),
	}
}

func NewFS(fsys fs.FS) *Registry {
	return &Registry{
		fsys:   fsys,
		agents: make(map[string]*Agent),
	}
}

func (r *Registry) Load(name string) (*Agent, error) {
	if a, ok := r.agents[name]; ok {
		return a, nil
	}

	defData, err := fs.ReadFile(r.fsys, filepath.Join(name, "def.yaml"))
	if err != nil {
		return nil, fmt.Errorf("агент %s не найден: %w", name, err)
	}

	var a Agent
	if err := yaml.Unmarshal(defData, &a); err != nil {
		return nil, fmt.Errorf("ошибка парсинга %s/def.yaml: %w", name, err)
	}

	if a.PromptFile != "" {
		if promptData, err := fs.ReadFile(r.fsys, filepath.Join(name, a.PromptFile)); err == nil {
			a.Prompt = string(promptData)
		}
	}

	r.agents[name] = &a
	return &a, nil
}

func (r *Registry) List() []*Agent {
	var result []*Agent
	entries, err := fs.ReadDir(r.fsys, ".")
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
