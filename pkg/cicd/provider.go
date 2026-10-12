// Package cicd contains controller-owned CI/CD deployment adapters. The
// controller triggers a build for an exact, already merged commit, waits for
// its final status and keeps the build URL as the stage result. Agents never
// receive provider credentials or call these adapters.
package cicd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Build states and statuses are provider-neutral.
const (
	StateQueued   = "queued"
	StateRunning  = "running"
	StateFinished = "finished"

	StatusSuccess  = "success"
	StatusFailure  = "failure"
	StatusCanceled = "canceled"
	StatusUnknown  = "unknown"
)

var (
	// ErrDeployRejected marks a deterministic negative result: the commit is
	// not merged, the build failed or built another revision. Retrying the same
	// request without a change cannot succeed.
	ErrDeployRejected = errors.New("deploy rejected")
	// ErrDeployTimeout marks an exhausted wait budget. The controller has
	// requested cancellation of the build.
	ErrDeployTimeout = errors.New("deploy wait timed out")
	// ErrNotMerged means the pull request is still open; the deploy waits.
	ErrNotMerged = errors.New("pull request is not merged yet")
)

// TransientError marks a failure that a status poll may retry until the
// deadline: network errors, HTTP 5xx and 429. It never wraps a trigger
// failure, because a repeated trigger could start a second deployment.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// HTTPStatusError is a provider answer with a non-2xx status. It carries only
// the method, API path and status code.
type HTTPStatusError struct {
	Method     string
	Path       string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
}

// IsDefiniteRejection reports whether the provider explicitly refused the
// request, so it had no effect: HTTP 4xx except 408 and 429. Everything else,
// including a 2xx answer that could not be read, leaves the outcome unknown.
func IsDefiniteRejection(err error) bool {
	var status *HTTPStatusError
	if !errors.As(err, &status) {
		return false
	}
	return status.StatusCode >= 400 && status.StatusCode < 500 &&
		status.StatusCode != 408 && status.StatusCode != 429
}

// IsTransient reports whether err may be retried by a status poll.
func IsTransient(err error) bool {
	var transient *TransientError
	return errors.As(err, &transient)
}

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidCommitSHA accepts only a full lowercase SHA-1 or SHA-256 object name.
func ValidCommitSHA(value string) bool { return commitSHAPattern.MatchString(value) }

// TriggerRequest identifies the exact merged commit to build and deploy.
type TriggerRequest struct {
	CommitSHA string
	Branch    string
}

// Build is the provider-neutral view of one CI/CD build.
type Build struct {
	ID         string `json:"id"`
	BuildType  string `json:"build_type,omitempty"`
	State      string `json:"state"`
	Status     string `json:"status,omitempty"`
	StatusText string `json:"status_text,omitempty"`
	WebURL     string `json:"web_url,omitempty"`
	Revision   string `json:"revision,omitempty"`
}

// Finished reports whether the provider will not change the build any more.
func (b Build) Finished() bool { return b.State == StateFinished }

// Provider is a CI/CD system adapter. Implementations MUST keep credentials
// out of returned errors and MUST validate URLs they return.
type Provider interface {
	Name() string
	Trigger(context.Context, TriggerRequest) (Build, error)
	Build(ctx context.Context, id string) (Build, error)
	Cancel(ctx context.Context, build Build, reason string) error
}

// Clock is injected so tests drive timeouts without sleeping.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock is the wall clock.
func SystemClock() Clock { return systemClock{} }

// WaitOptions bounds polling of one build.
type WaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
	Clock        Clock
	// CancelTimeout bounds the best-effort cancel request after timeout or
	// cancellation of the parent context.
	CancelTimeout time.Duration
}

// Wait polls the build until it finishes, the timeout elapses or ctx is
// canceled. On timeout or cancellation it asks the provider to cancel the
// build. Transient poll failures are retried until the deadline. A finished
// build succeeds only with status success and the exact expected revision.
func Wait(ctx context.Context, provider Provider, build Build, expectedSHA string, options WaitOptions) (Build, error) {
	if provider == nil {
		return build, errors.New("cicd provider is not configured")
	}
	if !ValidCommitSHA(expectedSHA) {
		return build, fmt.Errorf("%w: expected commit SHA is invalid", ErrDeployRejected)
	}
	if strings.TrimSpace(build.ID) == "" {
		return build, errors.New("cicd build id is empty")
	}
	clock := options.Clock
	if clock == nil {
		clock = SystemClock()
	}
	poll := options.PollInterval
	if poll <= 0 {
		poll = 15 * time.Second
	}
	deadline := clock.Now().Add(options.Timeout)
	for {
		if build.Finished() {
			return build, finishedBuildError(build, expectedSHA)
		}
		if options.Timeout > 0 && !clock.Now().Before(deadline) {
			cancelBuild(ctx, provider, build, "ai-team deploy wait timed out", options.CancelTimeout)
			return build, fmt.Errorf("%w after %s (build %s)", ErrDeployTimeout, options.Timeout, build.ID)
		}
		select {
		case <-ctx.Done():
			cancelBuild(ctx, provider, build, "ai-team deploy canceled", options.CancelTimeout)
			return build, fmt.Errorf("deploy wait for build %s: %w", build.ID, ctx.Err())
		case <-clock.After(poll):
		}
		next, err := provider.Build(ctx, build.ID)
		if err != nil {
			if ctx.Err() != nil {
				cancelBuild(ctx, provider, build, "ai-team deploy canceled", options.CancelTimeout)
				return build, fmt.Errorf("deploy wait for build %s: %w", build.ID, ctx.Err())
			}
			if IsTransient(err) {
				continue
			}
			return build, err
		}
		if next.ID != build.ID {
			return build, fmt.Errorf("cicd provider returned build %q instead of %q", next.ID, build.ID)
		}
		build = next
	}
}

func finishedBuildError(build Build, expectedSHA string) error {
	if build.Status != StatusSuccess {
		return fmt.Errorf("%w: build %s finished with status %s", ErrDeployRejected, build.ID, build.Status)
	}
	if build.Revision != expectedSHA {
		return fmt.Errorf("%w: build %s used revision %q, expected %s", ErrDeployRejected, build.ID, build.Revision, expectedSHA)
	}
	if strings.TrimSpace(build.WebURL) == "" {
		return fmt.Errorf("build %s has no web URL", build.ID)
	}
	return nil
}

func cancelBuild(ctx context.Context, provider Provider, build Build, reason string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	// Best effort: the wait result is already a failure and is reported.
	_ = provider.Cancel(cancelCtx, build, reason)
}
