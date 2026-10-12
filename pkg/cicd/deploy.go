package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const receiptSchemaVersion = 2

// Receipt is the controller-owned record of the build started for one deploy
// stage. It makes a retried stage resume or reuse the same build instead of
// triggering a duplicate deployment. It is only a pointer: every reuse
// re-reads the build from the provider.
type Receipt struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Stage         string    `json:"stage"`
	Provider      string    `json:"provider"`
	CommitSHA     string    `json:"commit_sha"`
	MergeCommit   string    `json:"merge_commit"`
	Branch        string    `json:"branch"`
	Build         Build     `json:"build"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DeployRequest describes one controller-initiated deployment.
type DeployRequest struct {
	RunID string
	Stage string
	// Merge identifies the controller pull request; Branch, Remote and
	// RepositoryDir also select the deploy branch.
	Merge MergeRequest
	// ReceiptPath is the controller state file for this run and stage.
	ReceiptPath string
	Runner      delivery.CommandRunner
	// MergeWait bounds waiting for the pull request merge.
	MergeWait WaitOptions
	// Wait bounds waiting for the build.
	Wait WaitOptions
}

// Deploy waits until the pull request is merged and proven by Git, then
// triggers (or resumes) the provider build for the deploy branch tip that
// contains it, and waits for the build's final status.
func Deploy(ctx context.Context, provider Provider, request DeployRequest) (Build, error) {
	if provider == nil {
		return Build{}, errors.New("cicd provider is not configured")
	}
	if request.RunID == "" || request.Stage == "" {
		return Build{}, errors.New("deploy request requires run and stage identity")
	}
	if request.Merge.Remote == "" {
		request.Merge.Remote = "origin"
	}
	proof, err := WaitForMerge(ctx, request.Runner, request.Merge, request.MergeWait)
	if err != nil {
		return Build{}, err
	}
	previous, found, err := ReadReceipt(request.ReceiptPath)
	if err != nil {
		return Build{}, err
	}
	if found && (previous.RunID != request.RunID || previous.Stage != request.Stage || previous.Provider != provider.Name()) {
		return Build{}, fmt.Errorf("%w: deploy receipt belongs to another run, stage or provider", ErrDeployRejected)
	}
	var build Build
	if found && previous.Build.ID != "" {
		// Never trust the stored status, link or build type: re-read the build.
		current, readErr := provider.Build(ctx, previous.Build.ID)
		if readErr != nil {
			return Build{}, readErr
		}
		if current.ID != previous.Build.ID {
			return Build{}, fmt.Errorf("cicd provider returned build %q instead of %q", current.ID, previous.Build.ID)
		}
		switch {
		case previous.CommitSHA != proof.DeploySHA:
			if !current.Finished() {
				// The branch moved on; replace the stale deployment.
				cancelBuild(ctx, provider, current, "ai-team deploys a newer branch tip", request.Wait.CancelTimeout)
			}
		case current.Finished() && finishedBuildError(current, proof.DeploySHA) == nil:
			if err := writeReceipt(request, provider.Name(), proof, current); err != nil {
				return current, err
			}
			return current, nil
		case !current.Finished():
			build = current
		}
	}
	if build.ID == "" {
		build, err = provider.Trigger(ctx, TriggerRequest{CommitSHA: proof.DeploySHA, Branch: request.Merge.Branch})
		if err != nil {
			return Build{}, err
		}
	}
	if err := writeReceipt(request, provider.Name(), proof, build); err != nil {
		// The build exists; stop it rather than leave an untracked deployment.
		cancelBuild(ctx, provider, build, "ai-team could not record the deploy build", request.Wait.CancelTimeout)
		return build, err
	}
	final, waitErr := Wait(ctx, provider, build, proof.DeploySHA, request.Wait)
	if err := writeReceipt(request, provider.Name(), proof, final); err != nil {
		return final, errors.Join(waitErr, err)
	}
	return final, waitErr
}

// ReadReceipt reads a deploy receipt; a missing file is not an error.
func ReadReceipt(path string) (Receipt, bool, error) {
	if strings.TrimSpace(path) == "" {
		return Receipt{}, false, errors.New("deploy receipt path is empty")
	}
	data, err := safeio.ReadRegularFile(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, fmt.Errorf("read deploy receipt: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.SchemaVersion != receiptSchemaVersion {
		return Receipt{}, false, errors.New("deploy receipt is invalid")
	}
	return receipt, true, nil
}

func writeReceipt(request DeployRequest, providerName string, proof MergeProof, build Build) error {
	if build.ID == "" {
		return nil
	}
	data, err := json.MarshalIndent(Receipt{
		SchemaVersion: receiptSchemaVersion, RunID: request.RunID, Stage: request.Stage,
		Provider: providerName, CommitSHA: proof.DeploySHA, MergeCommit: proof.MergeCommit,
		Branch: request.Merge.Branch, Build: build, UpdatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := safeio.ReplaceRegularFileNoFollow(request.ReceiptPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write deploy receipt: %w", err)
	}
	return nil
}
