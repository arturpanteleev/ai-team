package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gaterun"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_external.go — external_program and docker_program gates in the common
// gate chain (S07/S08 wired into the scheduler by S17).
//
// The program speaks ai-team.gate/v1 (pkg/gaterun). The gate runner here only
// translates: the stage result and the scheduler's attempt identity go in, the
// recorded attempt of pkg/gaterun comes out as an outcome. The classification
// stays in the runner: a valid `fail` is a succeeded execution with a negative
// verdict; a crash, a bad document, a lost process, a limit and an evidence
// violation are `error`, a timeout is `timeout`, a cancellation is `canceled`.
// A technical failure is never a pass and never a verdict.

// gateEvidenceDir is the directory of the run that holds the attempt records
// of external gates (stdout.log, stderr.log, invocation.json, evidence/…).
const gateEvidenceDir = "gates"

// externalGateRunner runs the gates of the kinds external and docker.
func (rs *runState) externalGateRunner(stage config.TemplateStage, result notifier.StageResult) gatesched.Runner {
	var (
		once   sync.Once
		runner *gaterun.Runner
		setup  error
	)
	return gatesched.RunnerFunc(func(ctx context.Context, in gatesched.Invocation) gatesched.Outcome {
		cfg, err := externalGateConfig(in)
		if err != nil {
			return gatesched.TechnicalError("конфигурация гейта: " + err.Error())
		}
		artifact, err := externalGateArtifact(stage, result, in.Attempt.ArtifactSHA256)
		if err != nil {
			return gatesched.TechnicalError(err.Error())
		}
		once.Do(func() {
			root, ensureErr := safeio.EnsureDir(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID, gateEvidenceDir)
			if ensureErr != nil {
				setup = ensureErr
				return
			}
			runner = &gaterun.Runner{EvidenceRoot: root}
		})
		if setup != nil {
			return gatesched.TechnicalError("каталог доказательств гейта: " + setup.Error())
		}
		attempt, err := runner.Run(ctx, gaterun.Request{
			Attempt: in.Attempt, Config: cfg, Artifact: artifact, RepoRoot: rs.runCfg.TargetDir,
		})
		if err != nil {
			// The request was invalid or the record could not be stored: the
			// gate has not produced an attempt, which is a technical failure.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return gatesched.Outcome{Execution: workflow.GateExecutionCanceled, Reason: ctxErr.Error()}
			}
			return gatesched.TechnicalError(err.Error())
		}
		return gatesched.Outcome{Execution: attempt.Execution, Verdict: attempt.Verdict, Reason: attempt.Reason, Evidence: attempt.Evidence}
	})
}

// externalGateConfig validates the trusted parameters of the reference by the
// kind of its definition.
func externalGateConfig(in gatesched.Invocation) (gaterun.Config, error) {
	switch in.Def.Kind {
	case workflow.GateKindExternal:
		return gaterun.ParseParams(in.Ref.Params)
	case workflow.GateKindDocker:
		return gaterun.ParseDockerParams(in.Ref.Params)
	}
	return gaterun.Config{}, fmt.Errorf("вид гейта %q не запускается как программа", in.Def.Kind)
}

// externalGateArtifact describes the checked result version for the program.
// A Markdown result is handed over as a readable file, a link result as its
// URL; an approval has neither.
func externalGateArtifact(stage config.TemplateStage, result notifier.StageResult, sha string) (gaterun.ArtifactInfo, error) {
	info := gaterun.ArtifactInfo{Type: stage.Result, SHA256: sha}
	switch stage.Result {
	case "md":
		for _, output := range result.Outputs {
			if output.Path != "" && isMarkdownOutput(output) {
				abs, err := filepath.Abs(output.Path)
				if err != nil {
					return info, fmt.Errorf("путь результата %q: %w", output.Name, err)
				}
				info.Path = abs
				return info, nil
			}
		}
		return info, errors.New("результат этапа не содержит md-файла для проверки")
	case "link":
		info.LinkKind = stage.LinkKind
		for _, output := range result.Outputs {
			if output.Path == "" {
				continue
			}
			data, err := safeio.ReadRegularFile(output.Path, 16<<10)
			if err != nil {
				return info, fmt.Errorf("результат %q не удалось прочитать: %w", output.Name, err)
			}
			info.URL = strings.TrimSpace(string(data))
			return info, nil
		}
		return info, errors.New("результат этапа не содержит ссылки для проверки")
	}
	return info, nil
}
