package cicd

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

// lostTriggerDeploy runs a deploy whose trigger answer is lost, then retries
// it with the given search behavior. It returns the retry result, the fake
// and the receipt written before the trigger.
func lostTriggerDeploy(t *testing.T, searchMode string) (Build, error, *fakeTeamCity, Receipt) {
	t.Helper()
	repo, prHead, tip := mergedRepo(t)
	fake := newFakeTeamCity(t, finished("SUCCESS", tip))
	fake.triggerMode = "drop"
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: repo.gh(prHead, merged(tip)),
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	if _, err := Deploy(context.Background(), fake.provider(t), request); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("lost trigger answer must leave the outcome unknown, got %v", err)
	}
	pending, found, err := ReadReceipt(request.ReceiptPath)
	if err != nil || !found || !pending.TriggerPending || !ValidDeployID(pending.DeployID) || pending.CommitSHA != tip {
		t.Fatalf("pending receipt must carry the deploy id: %+v %v %v", pending, found, err)
	}
	fake.mu.Lock()
	fake.triggerMode, fake.searchMode = "", searchMode
	fake.mu.Unlock()
	build, retryErr := Deploy(context.Background(), fake.provider(t), request)
	assertNoSecret(t, retryErr, fake)
	return build, retryErr, fake, pending
}

func TestDeployRecoversLostTriggerByDeployID(t *testing.T) {
	build, err, fake, pending := lostTriggerDeploy(t, "found")
	if err != nil || build.ID != "42" || build.Status != StatusSuccess {
		t.Fatalf("the build queued by the lost trigger must be recovered: %+v %v", build, err)
	}
	triggers, _ := fake.snapshot()
	if len(triggers) != 1 {
		t.Fatalf("recovery must not trigger again, got %d triggers", len(triggers))
	}
	fake.mu.Lock()
	searches := append([]string(nil), fake.searches...)
	fake.mu.Unlock()
	if len(searches) != 1 {
		t.Fatalf("expected one search, got %v", searches)
	}
	for _, want := range []string{"buildType:(id:Project_Deploy)", "property:(name:env.AI_TEAM_DEPLOY_ID,value:" + pending.DeployID + ",matchType:equals)", "state:any", "defaultFilter:false"} {
		if !strings.Contains(searches[0], want) {
			t.Fatalf("search locator %q lacks %q", searches[0], want)
		}
	}
	// The trigger carried exactly the ID recorded before it.
	properties := triggers[0]["properties"].(map[string]any)["property"].([]any)
	sent := ""
	for _, property := range properties {
		entry := property.(map[string]any)
		if entry["name"] == "env.AI_TEAM_DEPLOY_ID" {
			sent = entry["value"].(string)
		}
	}
	if sent != pending.DeployID {
		t.Fatalf("trigger sent deploy id %q, receipt recorded %q", sent, pending.DeployID)
	}
}

func TestDeployLostTriggerRecoveryNeverAdoptsAForeignBuild(t *testing.T) {
	for mode, want := range map[string]string{
		// Builds of the same configuration and revision without this request's
		// ID are not ours: nothing is found, so the retry still refuses.
		"foreignOnly":   "no build with its deploy id",
		"otherType":     "another build configuration",
		"otherRevision": "carries the deploy id but not revision",
		"twice":         "2 builds carry its deploy id",
		"":              "no build with its deploy id",
	} {
		t.Run(mode, func(t *testing.T) {
			_, err, fake, _ := lostTriggerDeploy(t, mode)
			if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), want) {
				t.Fatalf("search %q must be refused with %q, got %v", mode, want, err)
			}
			triggers, cancels := fake.snapshot()
			if len(triggers) != 1 || len(cancels) != 0 {
				t.Fatalf("a refused recovery must neither trigger nor cancel: triggers=%d cancels=%v", len(triggers), cancels)
			}
		})
	}
}

// noFinderProvider hides FindTriggered: a provider without search keeps the
// manual resolution of a pending trigger.
type noFinderProvider struct{ Provider }

// stubFinderProvider is a provider whose search returns fixed builds, so the
// re-check inside recoverPendingTrigger is exercised without trusting the
// provider-side filter.
type stubFinderProvider struct {
	Provider
	builds   []Build
	requests []TriggerRequest
}

func (p *stubFinderProvider) FindTriggered(_ context.Context, request TriggerRequest) ([]Build, error) {
	p.requests = append(p.requests, request)
	return p.builds, nil
}

func TestRecoverPendingTriggerRejectsBuildWithOtherRevision(t *testing.T) {
	recorded := strings.Repeat("a", 40)
	previous := Receipt{CommitSHA: recorded, Branch: "main", DeployID: testDeployID, TriggerPending: true}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	other := &stubFinderProvider{builds: []Build{{ID: "42", Revision: strings.Repeat("b", 40)}}}
	if build, err := recoverPendingTrigger(context.Background(), other, previous, receiptPath); !errors.Is(err, ErrDeployRejected) ||
		!strings.Contains(err.Error(), "does not match the recorded revision") || build.ID != "" {
		t.Fatalf("a found build on another revision must be refused: build=%+v err=%v", build, err)
	}
	if len(other.requests) != 1 || other.requests[0].CommitSHA != recorded || other.requests[0].DeployID != testDeployID {
		t.Fatalf("search must ask for the recorded revision and deploy id: %+v", other.requests)
	}
	same := &stubFinderProvider{builds: []Build{{ID: "42", Revision: recorded}}}
	if build, err := recoverPendingTrigger(context.Background(), same, previous, receiptPath); err != nil || build.ID != "42" {
		t.Fatalf("the build on the recorded revision must be recovered: build=%+v err=%v", build, err)
	}
}

func TestDeployPendingTriggerWithoutFinderOrIDStillRefuses(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	fake := newFakeTeamCity(t, finished("SUCCESS", tip))
	fake.searchMode = "found"
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: repo.gh(prHead, merged(tip)),
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	proof := MergeProof{MergeCommit: tip, DeploySHA: tip}
	if err := writeReceipt(request, "teamcity", proof, Build{}, true, testDeployID); err != nil {
		t.Fatal(err)
	}
	if _, err := Deploy(context.Background(), noFinderProvider{fake.provider(t)}, request); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "cannot search") {
		t.Fatalf("a provider without search must refuse, got %v", err)
	}
	// A receipt from before deploy IDs (or a tampered one) cannot be searched.
	for _, id := range []string{"", "not-hex", strings.Repeat("A", 32), testDeployID + ")"} {
		if err := writeReceipt(request, "teamcity", proof, Build{}, true, id); err != nil {
			t.Fatal(err)
		}
		if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "no valid deploy id") {
			t.Fatalf("deploy id %q must be refused, got %v", id, err)
		}
	}
	triggers, _ := fake.snapshot()
	fake.mu.Lock()
	searches := len(fake.searches)
	fake.mu.Unlock()
	if len(triggers) != 0 || searches != 0 {
		t.Fatalf("refused recovery reached TeamCity: triggers=%d searches=%d", len(triggers), searches)
	}
}

func TestDeployIDIsUniquePerTrigger(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	fake := newFakeTeamCity(t, finished("SUCCESS", tip))
	fake.triggerMode = "reject"
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: repo.gh(prHead, merged(tip)),
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	if _, err := Deploy(context.Background(), fake.provider(t), request); err == nil {
		t.Fatal("rejected trigger must fail")
	}
	fake.mu.Lock()
	fake.triggerMode = ""
	fake.mu.Unlock()
	if _, err := Deploy(context.Background(), fake.provider(t), request); err != nil {
		t.Fatal(err)
	}
	triggers, _ := fake.snapshot()
	if len(triggers) != 2 {
		t.Fatalf("expected two triggers, got %d", len(triggers))
	}
	ids := map[string]bool{}
	for _, trigger := range triggers {
		for _, property := range trigger["properties"].(map[string]any)["property"].([]any) {
			entry := property.(map[string]any)
			if entry["name"] == "env.AI_TEAM_DEPLOY_ID" {
				if !ValidDeployID(entry["value"].(string)) {
					t.Fatalf("invalid deploy id %v", entry["value"])
				}
				ids[entry["value"].(string)] = true
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("each trigger must carry its own deploy id, got %v", ids)
	}
	receipt, _, _ := ReadReceipt(request.ReceiptPath)
	if !ids[receipt.DeployID] || receipt.TriggerPending {
		t.Fatalf("receipt must keep the deploy id of its build: %+v", receipt)
	}
	if _, err := fake.provider(t).Trigger(context.Background(), TriggerRequest{CommitSHA: testSHA, Branch: "main"}); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("a trigger without deploy id must be refused, got %v", err)
	}
}

func TestTeamCityWebURLNormalizesDefaultPortAndHostCase(t *testing.T) {
	for _, base := range []string{"https://TeamCity.Example", "https://teamcity.example:443"} {
		provider, err := NewTeamCity(TeamCitySettings{BaseURL: base, BuildType: "Project_Deploy"}, testToken, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, accepted := range []string{"https://teamcity.example/build/1", "https://TEAMCITY.example:443/build/1", "https://teamcity.example:443/build/1"} {
			if _, err := provider.validateWebURL(accepted); err != nil {
				t.Fatalf("base %s: %s must be accepted: %v", base, accepted, err)
			}
		}
		for _, rejected := range []string{"https://teamcity.example:8443/build/1", "http://teamcity.example/build/1",
			"https://teamcity.example.evil/build/1", "https://evil.example/build/1", "https://teamcity.example:80/build/1",
			"https://user@teamcity.example/build/1", "https://teamcity.example/build/1#x", "https://:443/build/1"} {
			if _, err := provider.validateWebURL(rejected); err == nil {
				t.Fatalf("base %s: %s must be rejected", base, rejected)
			}
		}
	}
	base, _ := url.Parse("https://teamcity.example:8443")
	for candidate, want := range map[string]bool{
		"https://TEAMCITY.EXAMPLE:8443/x": true, "https://teamcity.example/x": false,
		"https://teamcity.example:443/x": false, "http://teamcity.example:8443/x": false,
	} {
		parsed, _ := url.Parse(candidate)
		if sameHTTPSOrigin(parsed, base) != want {
			t.Fatalf("same origin %s vs %s = %v", candidate, base, !want)
		}
	}
}

func TestOverlongRevisionIsBoundedInErrorsAndBuilds(t *testing.T) {
	long := testSHA + strings.Repeat("a", 10000)
	_, err := Wait(context.Background(), &stubProvider{}, Build{ID: "42", State: StateFinished, Status: StatusSuccess,
		Revision: long, WebURL: "https://teamcity.example/build/42"}, testSHA, waitOptions(&fakeClock{}))
	if !errors.Is(err, ErrDeployRejected) || len(err.Error()) > 400 || strings.Contains(err.Error(), long[:200]) {
		t.Fatalf("overlong revision must be rejected with a bounded message (len %d): %.300v", len(err.Error()), err)
	}

	fake := newFakeTeamCity(t, finished("SUCCESS", long))
	build, err := fake.provider(t).Build(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(build.Revision)) != maxRevisionDisplay+1 || !strings.HasSuffix(build.Revision, "…") || ValidCommitSHA(build.Revision) {
		t.Fatalf("stored revision must be bounded and invalid: %q", build.Revision)
	}
	// A revision that only starts with the expected SHA never matches it,
	// shortened or not.
	for _, revision := range []string{testSHA + "0", testSHA + strings.Repeat("0", maxRevisionDisplay)} {
		fake := newFakeTeamCity(t, finished("SUCCESS", revision))
		if _, err := Wait(context.Background(), fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{})); !errors.Is(err, ErrDeployRejected) {
			t.Fatalf("revision %q must be rejected, got %v", revision, err)
		}
	}
	if displayRevision(testSHA) != testSHA {
		t.Fatal("a valid SHA must be shown unchanged")
	}
}

// stubProvider answers nothing; Wait returns before polling a finished build.
type stubProvider struct{}

func (stubProvider) Name() string                                           { return "stub" }
func (stubProvider) Trigger(context.Context, TriggerRequest) (Build, error) { return Build{}, nil }
func (stubProvider) Build(context.Context, string) (Build, error)           { return Build{}, nil }
func (stubProvider) Cancel(context.Context, Build, string) error            { return nil }

func TestIsolatedGitDropsTemplateDirectory(t *testing.T) {
	if _, err := exec.LookPath("env"); err != nil {
		skipOutsideCI(t, "env is unavailable")
	}
	template := t.TempDir()
	t.Setenv("GIT_TEMPLATE_DIR", template)
	result := isolatedGitRunner(t.TempDir(), "!helper", nil).Run(context.Background(), t.TempDir(), "env")
	if result.Status != delivery.StepPassed || strings.Contains(result.Stdout, "GIT_TEMPLATE_DIR") {
		t.Fatalf("GIT_TEMPLATE_DIR must not reach isolated git (status %s)", result.Status)
	}
}

// initRecorder runs Git in the real isolated environment and inspects the
// private repository right after git init.
type initRecorder struct {
	mu       sync.Mutex
	initArgs []string
	entries  []string
}

func (r *initRecorder) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	result := isolatedGitRunner(dir, "!true", nil).Run(ctx, dir, name, args...)
	if name == "git" && len(args) > 0 && args[0] == "init" {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.initArgs = append([]string(nil), args...)
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			r.entries = append(r.entries, entry.Name())
		}
	}
	return result
}

func TestResolveMergedDeployInitsWithoutTemplate(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	template := t.TempDir()
	if err := os.MkdirAll(filepath.Join(template, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "planted"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_TEMPLATE_DIR", template)
	recorder := &initRecorder{}
	request := repo.request(prHead)
	request.GitRunner = recorder
	proof, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(tip)), request)
	if err != nil || proof.DeploySHA != tip {
		t.Fatalf("deploy proof: %+v %v", proof, err)
	}
	if strings.Join(recorder.initArgs, " ") != "init --bare --quiet --template=" {
		t.Fatalf("private repository must be created without a template: %v", recorder.initArgs)
	}
	for _, entry := range recorder.entries {
		if entry == "planted" || entry == "hooks" || entry == "description" {
			t.Fatalf("template content %q was copied into the private repository: %v", entry, recorder.entries)
		}
	}
}

// dirRecorder records the working directory of every gh call.
type dirRecorder struct {
	inner delivery.CommandRunner
	mu    sync.Mutex
	dirs  []string
	empty []bool
}

func (r *dirRecorder) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	if name == "gh" {
		entries, err := os.ReadDir(dir)
		r.mu.Lock()
		r.dirs = append(r.dirs, dir)
		r.empty = append(r.empty, err == nil && len(entries) == 0)
		r.mu.Unlock()
	}
	return r.inner.Run(ctx, dir, name, args...)
}

func TestResolveMergedDeployRunsGhInPrivateDirectory(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	// The controller may run inside the agent-writable checkout.
	t.Chdir(repo.work)
	recorder := &dirRecorder{inner: repo.gh(prHead, merged(tip))}
	if _, err := ResolveMergedDeploy(context.Background(), recorder, repo.request(prHead)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.dirs) != 2 {
		t.Fatalf("expected gh pr view and gh api, got %v", recorder.dirs)
	}
	work, _ := filepath.EvalSymlinks(repo.work)
	for index, dir := range recorder.dirs {
		resolved, _ := filepath.EvalSymlinks(dir)
		if dir == "" || !filepath.IsAbs(dir) || !recorder.empty[index] || strings.HasPrefix(resolved, work) {
			t.Fatalf("gh must run in a private empty directory, got %q (empty %v)", dir, recorder.empty[index])
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("private gh directory %s must be removed after the check", dir)
		}
	}
}

// blockingGh blocks the first gh pr view until its context ends, like a hung
// network call, then answers normally.
type blockingGh struct {
	inner   delivery.CommandRunner
	mu      sync.Mutex
	blocked int
	views   int
}

func (b *blockingGh) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	if name == "gh" && args[0] == "pr" {
		b.mu.Lock()
		b.views++
		first := b.views == 1
		b.mu.Unlock()
		if first {
			<-ctx.Done()
			b.mu.Lock()
			b.blocked++
			b.mu.Unlock()
			return delivery.StepResult{Status: delivery.StepFailed, ExitCode: -1}
		}
	}
	return b.inner.Run(ctx, dir, name, args...)
}

func TestWaitForMergeBoundsEachCheck(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	runner := &blockingGh{inner: repo.gh(prHead, merged(tip))}
	options := waitOptions(&fakeClock{})
	// Long enough for a real fetch and proof under -race, short for a hang.
	options.AttemptTimeout = 5 * time.Second
	done := make(chan struct{})
	var proof MergeProof
	var err error
	go func() {
		defer close(done)
		proof, err = WaitForMerge(context.Background(), runner, repo.request(prHead), options)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("a hung merge check was not bounded by its own deadline")
	}
	if err != nil || proof.DeploySHA != tip || runner.blocked != 1 || runner.views != 2 {
		t.Fatalf("a timed-out check must be retried: %+v %v blocked=%d views=%d", proof, err, runner.blocked, runner.views)
	}

	// The parent context still wins: cancellation is not a transient failure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveMergedDeployWithin(ctx, &blockingGh{inner: repo.gh(prHead, merged(tip))}, repo.request(prHead), time.Minute); !errors.Is(err, context.Canceled) || IsTransient(err) {
		t.Fatalf("parent cancellation must not become transient, got %v", err)
	}
}

func TestIsolatedGitEnvironmentLimitsStalledFetch(t *testing.T) {
	env := strings.Join(isolatedGitEnv("/tmp/x.git", "!helper"), "\n")
	for _, want := range []string{"=http.lowSpeedLimit\n", "=" + gitLowSpeedLimit + "\n", "=http.lowSpeedTime\n", "=" + gitLowSpeedTime} {
		if !strings.Contains(env, want) {
			t.Fatalf("isolated git environment lacks %q:\n%s", want, env)
		}
	}
	if !strings.Contains(env, "GIT_CONFIG_COUNT=6") {
		t.Fatalf("GIT_CONFIG_COUNT must cover the stall limits:\n%s", env)
	}
}
