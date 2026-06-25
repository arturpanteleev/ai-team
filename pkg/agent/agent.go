package agent

type Agent struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	RuntimeType string            `yaml:"runtime"`
	CLI         string            `yaml:"cli"`
	PromptFile  string            `yaml:"prompt_file"`
	Prompt      string            `yaml:"-"`
	Inputs      map[string]string `yaml:"inputs"`
	Outputs     map[string]string `yaml:"outputs"`
}
