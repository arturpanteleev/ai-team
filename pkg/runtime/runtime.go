package runtime

import (
	"context"
	"errors"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var ErrNotImplemented = errors.New("LLM runtime: not implemented yet")

type Runtime interface {
	Execute(ctx context.Context, agent *Agent, task *Task, inputs []Artifact) error
}

// Artifact is kept as a compatibility alias; the domain type lives in workflow.
type Artifact = workflow.Artifact

// Factory создаёт runtime по типу из def.yaml (инжектируется в пайплайн
// для тестируемости).
type Factory func(runtimeType string) (Runtime, error)

type Agent struct {
	Name         string
	AttemptID    string
	RuntimeType  string
	CLI          string
	Model        string
	Effort       string
	Prompt       string
	Inputs       map[string]string
	Outputs      map[string]string
	Verdict      *verdict.Contract
	Kind         string
	Mutation     string
	AllowedPaths []string
	RequireDiff  bool
}

type Task struct {
	Feature      string
	TaskDesc     string
	TargetDir    string
	ArtifactRoot string
	// LogDir — каталог логов агентов; пусто = логирование выключено.
	LogDir string
	// ConsoleOut — куда дублировать вывод агента (по умолчанию os.Stdout).
	ConsoleOut io.Writer
}
