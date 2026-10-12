package cicd

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// ValidBranch accepts a conservative subset of Git branch names that cannot be
// parsed as an option or a revision expression.
func ValidBranch(value string) bool {
	return branchPattern.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.Contains(value, "//") && !strings.HasSuffix(value, "/") &&
		!strings.HasSuffix(value, ".lock") && !strings.HasSuffix(value, ".")
}

// VerifyMergedCommit proves through local Git, not through any provider
// response, that commitSHA is a commit reachable from the freshly fetched
// remote branch. Command diagnostics are not included in errors because Git
// output can contain remote URLs with credentials.
func VerifyMergedCommit(ctx context.Context, runner delivery.CommandRunner, dir, remote, branch, commitSHA string) error {
	if runner == nil {
		runner = delivery.ExecRunner{}
	}
	if !ValidCommitSHA(commitSHA) {
		return fmt.Errorf("%w: expected commit SHA is invalid", ErrDeployRejected)
	}
	if !ValidBranch(branch) || !ValidBranch(remote) {
		return fmt.Errorf("%w: deploy branch or remote is invalid", ErrDeployRejected)
	}
	tracking := "refs/remotes/" + remote + "/" + branch
	fetch := runner.Run(ctx, dir, "git", "fetch", "--no-tags", "--quiet", remote, "+refs/heads/"+branch+":"+tracking)
	if fetch.Status != delivery.StepPassed {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("git fetch %s: %w", branch, err)
		}
		return fmt.Errorf("git fetch of deploy branch %s failed (exit %d)", branch, fetch.ExitCode)
	}
	resolved := runner.Run(ctx, dir, "git", "rev-parse", "--verify", "--quiet", commitSHA+"^{commit}")
	if resolved.Status != delivery.StepPassed {
		return fmt.Errorf("%w: commit %s is not present in the local repository", ErrDeployRejected, commitSHA)
	}
	if strings.TrimSpace(resolved.Stdout) != commitSHA {
		return fmt.Errorf("%w: commit %s resolves to another object", ErrDeployRejected, commitSHA)
	}
	ancestor := runner.Run(ctx, dir, "git", "merge-base", "--is-ancestor", commitSHA, tracking)
	switch {
	case ancestor.Status == delivery.StepPassed:
		return nil
	case ancestor.ExitCode == 1:
		return fmt.Errorf("%w: commit %s is not merged into %s/%s", ErrDeployRejected, commitSHA, remote, branch)
	default:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("git merge-base: %w", err)
		}
		return fmt.Errorf("git merge-base for deploy commit failed (exit %d)", ancestor.ExitCode)
	}
}
