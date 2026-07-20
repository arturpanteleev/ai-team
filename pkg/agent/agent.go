package agent

import (
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

type Agent struct {
	Name          string                       `yaml:"name"`
	Description   string                       `yaml:"description"`
	RuntimeType   string                       `yaml:"runtime"`
	CLI           string                       `yaml:"cli"`
	PromptFile    string                       `yaml:"prompt_file"`
	Prompt        string                       `yaml:"-"`
	Source        string                       `yaml:"-"`
	Inputs        map[string]string            `yaml:"inputs"`
	Outputs       map[string]string            `yaml:"outputs"`
	Verdict       *verdict.Contract            `yaml:"verdict,omitempty"`
	Preconditions map[string]*verdict.Contract `yaml:"preconditions,omitempty"`
	Kind          string                       `yaml:"kind,omitempty"`
	Mutation      string                       `yaml:"mutation,omitempty"`
	AllowedPaths  []string                     `yaml:"allowed_paths,omitempty"`
	RequireDiff   bool                         `yaml:"require_diff,omitempty"`
	Checks        []checks.Definition          `yaml:"checks,omitempty"`
}
