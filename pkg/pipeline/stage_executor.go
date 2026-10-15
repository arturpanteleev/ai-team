package pipeline

import (
	"fmt"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// stage_executor.go — выбор исполнителя этапа (внутренний агент или именованный
// внешний адаптер) и сборка запроса к нему. Контроллер после исполнителя
// одинаково проверяет тип результата, mutation scope и verdict: исполнитель
// только создаёт выходы и не меняет граф.

// ExternalExecutorFactory builds the executor for a configured adapter name.
// The default runs the argv on the host; a container executor replaces it.
type ExternalExecutorFactory func(name string, spec stageexec.AdapterSpec) (stageexec.StageExecutor, error)

// WithExternalExecutorFactory overrides how named adapters are launched (tests,
// container executors).
func WithExternalExecutorFactory(factory ExternalExecutorFactory) Option {
	return func(p *Pipeline) { p.externalExecutors = factory }
}

// stageAdapter returns the external adapter configured for the stage or "".
func (p *Pipeline) stageAdapter(stageID string) string {
	if stage, ok := p.templateStage(stageID); ok {
		return stage.Adapter
	}
	return ""
}

func (p *Pipeline) newExternalExecutor(name string) (stageexec.StageExecutor, error) {
	spec, ok := p.cfg.ExecutorAdapters[name]
	if !ok {
		return nil, fmt.Errorf("внешний исполнитель %q не объявлен в executor_adapters", name)
	}
	if p.externalExecutors != nil {
		return p.externalExecutors(name, spec)
	}
	return stageexec.NewExternal(name, spec), nil
}

// stageExecutorRequest resolves the declared outputs to absolute artifact
// paths confined to the artifact root and collects the stage contract.
func (rs *runState) stageExecutorRequest(a *agent.Agent, stageID, attemptID string, task *runtime.Task, inputs []runtime.Artifact) (stageexec.Request, error) {
	outputs := make([]stageexec.Output, 0, len(a.Outputs))
	for _, outputName := range sortedStringMapKeys(a.Outputs) {
		path, err := confinedArtifactPath(rs.task.ArtifactRoot, runtime.ReplaceVars(a.Outputs[outputName], rs.runCfg.Feature))
		if err != nil {
			return stageexec.Request{}, fmt.Errorf("агент %s: output %s: %w", stageID, outputName, err)
		}
		outputs = append(outputs, stageexec.Output{Name: outputName, Path: path})
	}
	artifactRoot, err := filepath.Abs(rs.task.ArtifactRoot)
	if err != nil {
		return stageexec.Request{}, fmt.Errorf("агент %s: artifact root: %w", stageID, err)
	}
	return stageexec.Request{
		RunID: rs.runID, AttemptID: attemptID, Stage: stageID, Feature: task.Feature, Task: task.TaskDesc,
		Instructions: a.Prompt, WorkDir: rs.sourceDir(), ArtifactRoot: artifactRoot, LogDir: task.LogDir,
		Inputs: inputs, Outputs: outputs, Mutation: a.Mutation, AllowedPaths: append([]string(nil), a.AllowedPaths...),
	}, nil
}

// executorInfo converts the executor's provenance to its evidence form.
func executorInfo(result stageexec.Result) *workflow.ExecutorInfo {
	if result.Identity.Kind == "" {
		return nil
	}
	info := &workflow.ExecutorInfo{
		Kind: result.Identity.Kind, Name: result.Identity.Name,
		Summary: result.Summary, Metadata: result.Metadata,
	}
	if result.LogPath != "" {
		info.LogFile = filepath.Base(result.LogPath)
	}
	return info
}
