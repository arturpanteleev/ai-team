package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/cicd"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

const deployRemote = "origin"

// WithDeployProvider injects the controller-owned CI/CD provider. Only the
// local controller process configures it; a sandboxed worker never receives
// provider credentials, so a deploy stage there fails closed.
func WithDeployProvider(provider cicd.Provider) Option {
	return func(p *Pipeline) { p.deployProvider = provider }
}

// WithDeployClock replaces the wall clock used by the deploy wait (tests).
func WithDeployClock(clock cicd.Clock) Option {
	return func(p *Pipeline) { p.deployClock = clock }
}

// WithDeployCommandRunner replaces the Git runner used to prove that the
// deployed commit is merged (tests).
func WithDeployCommandRunner(runner delivery.CommandRunner) Option {
	return func(p *Pipeline) { p.deployRunner = runner }
}

// runControllerDeploy is the body of a kind=deploy stage. The controller, not
// a model, proves that the commit published by the earlier PR stage is merged,
// triggers the configured CI/CD build for exactly that commit, waits for its
// final status and writes the build URL as the stage's link output.
func (rs *runState) runControllerDeploy(ctx context.Context, name, attemptID string, a *agent.Agent) error {
	stage, ok := rs.p.templateStage(name)
	if !ok || stage.Result != "link" || stage.LinkKind != "build" {
		return fmt.Errorf("%w: deploy stage %s must declare result link with link_kind build", cicd.ErrDeployRejected, name)
	}
	if rs.p.cfg == nil || rs.p.cfg.CICD == nil || rs.p.cfg.CICD.TeamCity == nil {
		return fmt.Errorf("%w: cicd.teamcity is not configured for deploy stage %s", cicd.ErrDeployRejected, name)
	}
	settings := rs.p.cfg.CICD.TeamCity
	provider := rs.p.deployProvider
	if provider == nil {
		return errors.New("CI/CD provider is unavailable in this process: deploy runs only in the local controller")
	}
	receipt, err := rs.ensureStageDeliveryReceipt(ctx)
	if err != nil {
		return fmt.Errorf("deploy commit: %w", err)
	}
	if receipt == nil || strings.TrimSpace(receipt.CommitSHA) == "" {
		return fmt.Errorf("%w: deploy requires a controller PR delivery receipt with a commit SHA from an earlier stage", cicd.ErrDeployRejected)
	}
	commitSHA := strings.ToLower(strings.TrimSpace(receipt.CommitSHA))
	timeout, err := settings.EffectiveTimeout()
	if err != nil {
		return err
	}
	pollInterval, err := settings.EffectivePollInterval()
	if err != nil {
		return err
	}
	stateDir, err := safeio.EnsureDir(rs.runCfg.TargetDir, ".ai-team", "state", "deploy")
	if err != nil {
		return fmt.Errorf("deploy state directory: %w", err)
	}
	logging.Printf("  %s выкладка %s через %s: commit %s (%s)\n",
		ui.Colorize("⇪", ui.ColorCyan), name, provider.Name(), commitSHA, settings.Branch)
	build, err := cicd.Deploy(ctx, provider, cicd.DeployRequest{
		RepositoryDir: rs.runCfg.TargetDir, Remote: deployRemote, Branch: settings.Branch,
		CommitSHA: commitSHA, ReceiptPath: filepath.Join(stateDir, rs.runID+"-"+name+".json"),
		Runner: rs.p.deployRunner,
		Wait:   cicd.WaitOptions{Timeout: timeout, PollInterval: pollInterval, Clock: rs.p.deployClock},
	})
	if err != nil {
		rs.recordDeployIntegrationError(name, attemptID, provider.Name(), err)
		return fmt.Errorf("deploy %s: %w", provider.Name(), err)
	}
	relative, ok := a.Outputs["link"]
	if !ok {
		return errors.New("deploy agent has no link output")
	}
	outputPath, err := confinedArtifactPath(rs.task.ArtifactRoot, runtime.ReplaceVars(relative, rs.runCfg.Feature))
	if err != nil {
		return err
	}
	if err := safeio.EnsureDirPath(filepath.Dir(outputPath)); err != nil {
		return fmt.Errorf("deploy link directory: %w", err)
	}
	if err := safeio.WriteRegularFileNoFollow(outputPath, []byte(build.WebURL+"\n"), 0o644); err != nil {
		return fmt.Errorf("write deploy link: %w", err)
	}
	logging.Printf("  %s сборка %s: %s\n", ui.Colorize("✓", ui.ColorGreen), build.ID, build.WebURL)
	return nil
}

// recordDeployIntegrationError projects a deploy failure to the task event
// feed as a warning. Errors produced by the CI/CD adapters never contain the
// access token or request URLs.
func (rs *runState) recordDeployIntegrationError(stage, attemptID, provider string, err error) {
	rs.recordTypedEvent(evidence.Event{
		Type: "integration_error", Stage: stage, AttemptID: attemptID, Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"integration": "cicd", "provider": provider, "error": err.Error(), "severity": "warning",
		},
	})
}
