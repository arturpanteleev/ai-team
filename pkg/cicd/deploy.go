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

const receiptSchemaVersion = 1

// Receipt is the controller-owned record of the build started for one deploy
// stage. It makes a retried stage resume or reuse the same build instead of
// triggering a duplicate deployment.
type Receipt struct {
	SchemaVersion int       `json:"schema_version"`
	Provider      string    `json:"provider"`
	CommitSHA     string    `json:"commit_sha"`
	Branch        string    `json:"branch"`
	Build         Build     `json:"build"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DeployRequest describes one controller-initiated deployment.
type DeployRequest struct {
	// RepositoryDir is the local Git repository used to prove the merge.
	RepositoryDir string
	Remote        string
	Branch        string
	CommitSHA     string
	// ReceiptPath is the controller state file for this run and stage.
	ReceiptPath string
	Runner      delivery.CommandRunner
	Wait        WaitOptions
}

// Deploy proves that the expected commit is merged, then triggers (or resumes)
// the provider build for exactly that commit and waits for its final status.
func Deploy(ctx context.Context, provider Provider, request DeployRequest) (Build, error) {
	if provider == nil {
		return Build{}, errors.New("cicd provider is not configured")
	}
	if request.Remote == "" {
		request.Remote = "origin"
	}
	if err := VerifyMergedCommit(ctx, request.Runner, request.RepositoryDir, request.Remote, request.Branch, request.CommitSHA); err != nil {
		return Build{}, err
	}
	previous, found, err := ReadReceipt(request.ReceiptPath)
	if err != nil {
		return Build{}, err
	}
	var build Build
	if found && previous.Provider == provider.Name() && previous.CommitSHA == request.CommitSHA && previous.Branch == request.Branch {
		switch {
		case previous.Build.Finished() && finishedBuildError(previous.Build, request.CommitSHA) == nil:
			return previous.Build, nil
		case !previous.Build.Finished() && previous.Build.ID != "":
			build, err = provider.Build(ctx, previous.Build.ID)
			if err != nil {
				return Build{}, err
			}
			if build.ID != previous.Build.ID {
				return Build{}, fmt.Errorf("cicd provider returned build %q instead of %q", build.ID, previous.Build.ID)
			}
		}
	}
	if build.ID == "" {
		build, err = provider.Trigger(ctx, TriggerRequest{CommitSHA: request.CommitSHA, Branch: request.Branch})
		if err != nil {
			return Build{}, err
		}
	}
	if err := writeReceipt(request.ReceiptPath, provider.Name(), request, build); err != nil {
		// The build exists; stop it rather than leave an untracked deployment.
		cancelBuild(ctx, provider, build, "ai-team could not record the deploy build", request.Wait.CancelTimeout)
		return build, err
	}
	final, waitErr := Wait(ctx, provider, build, request.CommitSHA, request.Wait)
	if err := writeReceipt(request.ReceiptPath, provider.Name(), request, final); err != nil {
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

func writeReceipt(path, providerName string, request DeployRequest, build Build) error {
	if build.ID == "" {
		return nil
	}
	data, err := json.MarshalIndent(Receipt{
		SchemaVersion: receiptSchemaVersion, Provider: providerName,
		CommitSHA: request.CommitSHA, Branch: request.Branch, Build: build,
		UpdatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := safeio.ReplaceRegularFileNoFollow(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write deploy receipt: %w", err)
	}
	return nil
}
