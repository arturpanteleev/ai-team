package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

const (
	testToken = "tc-secret-token-7f3a"
	testSHA   = "0123456789abcdef0123456789abcdef01234567"
	otherSHA  = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakeTeamCity is an in-memory TeamCity REST stub. Each GET of the build
// advances through states; the last state repeats.
type fakeTeamCity struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	states   []map[string]any
	polls    int
	triggers []map[string]any
	cancels  []string
	authOK   bool
	webHost  string
	// unavailable answers the first N status polls with HTTP 503.
	unavailable int
}

func newFakeTeamCity(t *testing.T, states ...map[string]any) *fakeTeamCity {
	t.Helper()
	fake := &fakeTeamCity{t: t, states: states, authOK: true}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	fake.webHost = strings.TrimPrefix(fake.server.URL, "https://")
	return fake
}

func (f *fakeTeamCity) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.authOK = false
		http.Error(w, "unauthorized: token "+r.Header.Get("Authorization"), http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/rest/buildQueue":
		var trigger map[string]any
		if err := json.Unmarshal(body, &trigger); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.triggers = append(f.triggers, trigger)
		f.writeBuild(w, map[string]any{"state": "queued"})
	case r.Method == http.MethodGet && r.URL.Path == "/app/rest/builds/id:42":
		if f.unavailable > 0 {
			f.unavailable--
			http.Error(w, "maintenance", http.StatusServiceUnavailable)
			return
		}
		state := f.states[min(f.polls, len(f.states)-1)]
		f.polls++
		f.writeBuild(w, state)
	case r.Method == http.MethodPost && (r.URL.Path == "/app/rest/builds/id:42" || r.URL.Path == "/app/rest/buildQueue/id:42"):
		f.cancels = append(f.cancels, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTeamCity) writeBuild(w http.ResponseWriter, fields map[string]any) {
	build := map[string]any{"id": 42, "buildTypeId": "Project_Deploy", "webUrl": "https://" + f.webHost + "/buildConfiguration/Deploy/42"}
	for key, value := range fields {
		build[key] = value
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(build)
}

func (f *fakeTeamCity) provider(t *testing.T) *TeamCity {
	t.Helper()
	provider, err := NewTeamCity(TeamCitySettings{BaseURL: f.server.URL + "/", BuildType: "Project_Deploy"}, testToken, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func (f *fakeTeamCity) snapshot() (triggers []map[string]any, cancels []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.triggers...), append([]string(nil), f.cancels...)
}

func finished(status, revision string) map[string]any {
	return map[string]any{"state": "finished", "status": status,
		"revisions": map[string]any{"revision": []map[string]any{{"version": revision}}}}
}

// fakeClock advances virtual time on every After call; blocking clocks never
// fire, which leaves cancellation as the only way out of the wait.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	block bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.block {
		return nil
	}
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func waitOptions(clock Clock) WaitOptions {
	return WaitOptions{Timeout: time.Minute, PollInterval: 15 * time.Second, Clock: clock}
}

func assertNoSecret(t *testing.T, err error, fake *fakeTeamCity) {
	t.Helper()
	if err == nil {
		return
	}
	message := err.Error()
	if strings.Contains(message, testToken) || strings.Contains(message, fake.webHost) {
		t.Fatalf("error leaks token or server URL: %s", message)
	}
}

func TestTeamCityDeploySucceedsForExactRevision(t *testing.T) {
	fake := newFakeTeamCity(t, map[string]any{"state": "running"}, finished("SUCCESS", testSHA))
	provider := fake.provider(t)
	build, err := provider.Trigger(context.Background(), TriggerRequest{CommitSHA: testSHA, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := Wait(context.Background(), provider, build, testSHA, waitOptions(&fakeClock{}))
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if final.Status != StatusSuccess || final.Revision != testSHA || !strings.HasSuffix(final.WebURL, "/buildConfiguration/Deploy/42") {
		t.Fatalf("unexpected build: %+v", final)
	}
	triggers, cancels := fake.snapshot()
	fake.mu.Lock()
	authOK := fake.authOK
	fake.mu.Unlock()
	if len(triggers) != 1 || len(cancels) != 0 || !authOK {
		t.Fatalf("triggers=%v cancels=%v auth=%v", triggers, cancels, authOK)
	}
	encoded, _ := json.Marshal(triggers[0])
	for _, want := range []string{`"id":"Project_Deploy"`, `"branchName":"main"`, `"version":"` + testSHA + `"`, `"vcsBranchName":"refs/heads/main"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("trigger body %s lacks %s", encoded, want)
		}
	}
}

func TestTeamCityFailedBuildIsRejected(t *testing.T) {
	fake := newFakeTeamCity(t, finished("FAILURE", testSHA))
	provider := fake.provider(t)
	_, err := Wait(context.Background(), provider, Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{}))
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("failed build must be rejected, got %v", err)
	}
	assertNoSecret(t, err, fake)
}

func TestTeamCityWrongRevisionIsRejected(t *testing.T) {
	fake := newFakeTeamCity(t, finished("SUCCESS", otherSHA))
	_, err := Wait(context.Background(), fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{}))
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), otherSHA) {
		t.Fatalf("build of another revision must be rejected, got %v", err)
	}
}

func TestTeamCityWaitTimeoutCancelsBuild(t *testing.T) {
	fake := newFakeTeamCity(t, map[string]any{"state": "running"})
	clock := &fakeClock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	_, err := Wait(context.Background(), fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(clock))
	if !errors.Is(err, ErrDeployTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
	_, cancels := fake.snapshot()
	if len(cancels) != 1 || cancels[0] != "/app/rest/builds/id:42" {
		t.Fatalf("running build must be canceled once, got %v", cancels)
	}
	fake.mu.Lock()
	polls := fake.polls
	fake.mu.Unlock()
	if polls != 4 {
		t.Fatalf("1m timeout with 15s polling must poll 4 times, got %d", polls)
	}
}

func TestTeamCityWaitCancellationCancelsQueuedBuild(t *testing.T) {
	fake := newFakeTeamCity(t, map[string]any{"state": "queued"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Wait(ctx, fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{block: true}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	_, cancels := fake.snapshot()
	if len(cancels) != 1 || cancels[0] != "/app/rest/buildQueue/id:42" {
		t.Fatalf("queued build must be removed from the queue, got %v", cancels)
	}
}

func TestTeamCityRejectsRedirectToForeignHost(t *testing.T) {
	foreignHits := 0
	var foreignAuth string
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignHits++
		foreignAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/app/rest/buildQueue?token="+testToken, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	provider, err := NewTeamCity(TeamCitySettings{BaseURL: origin.URL, BuildType: "Project_Deploy"}, testToken, origin.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Trigger(context.Background(), TriggerRequest{CommitSHA: testSHA, Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("foreign redirect must fail, got %v", err)
	}
	if foreignHits != 0 || foreignAuth != "" {
		t.Fatalf("foreign host received %d requests (auth %q)", foreignHits, foreignAuth)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), foreign.URL) || strings.Contains(err.Error(), origin.URL) {
		t.Fatalf("redirect error leaks token or URL: %v", err)
	}
}

func TestTeamCityErrorsNeverContainToken(t *testing.T) {
	fake := newFakeTeamCity(t, finished("SUCCESS", testSHA))
	provider, err := NewTeamCity(TeamCitySettings{BaseURL: fake.server.URL, BuildType: "Project_Deploy"}, testToken+"-wrong", fake.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Trigger(context.Background(), TriggerRequest{CommitSHA: testSHA, Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("expected HTTP 401, got %v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error leaks token or response body: %v", err)
	}
	assertNoSecret(t, err, fake)

	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closedURL, client := closed.URL, closed.Client()
	closed.Close()
	provider, err = NewTeamCity(TeamCitySettings{BaseURL: closedURL, BuildType: "Project_Deploy"}, testToken, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Build(context.Background(), "42")
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), closedURL) {
		t.Fatalf("network error must not contain token or URL: %v", err)
	}
}

func TestTeamCityRejectsUnsafeConfigurationAndLinks(t *testing.T) {
	for _, base := range []string{"http://teamcity.example", "https://user:pw@teamcity.example", "https://teamcity.example/?a=1", "teamcity.example"} {
		if _, err := NewTeamCity(TeamCitySettings{BaseURL: base, BuildType: "Deploy"}, testToken, nil); err == nil {
			t.Fatalf("base URL %q must be rejected", base)
		}
	}
	if _, err := NewTeamCity(TeamCitySettings{BaseURL: "https://teamcity.example", BuildType: "Deploy"}, " ", nil); err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("empty token must be rejected: %v", err)
	}
	if _, err := NewTeamCity(TeamCitySettings{BaseURL: "https://teamcity.example", BuildType: "../x"}, testToken, nil); err == nil {
		t.Fatal("invalid build type must be rejected")
	}
	fake := newFakeTeamCity(t, map[string]any{"state": "finished", "status": "SUCCESS", "webUrl": "https://evil.example/build/42"})
	_, err := fake.provider(t).Build(context.Background(), "42")
	if err == nil || !strings.Contains(err.Error(), "configured TeamCity host") {
		t.Fatalf("foreign web URL must be rejected, got %v", err)
	}
	if _, err := fake.provider(t).Build(context.Background(), "42/../../x"); err == nil {
		t.Fatal("non-numeric build id must be rejected")
	}
}

func TestTeamCityWaitRetriesTransientPollFailures(t *testing.T) {
	fake := newFakeTeamCity(t, finished("SUCCESS", testSHA))
	fake.unavailable = 2
	final, err := Wait(context.Background(), fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{}))
	if err != nil || final.Status != StatusSuccess {
		t.Fatalf("503 during polling must be retried until the deadline: %+v %v", final, err)
	}
	fake.unavailable = 100
	_, err = Wait(context.Background(), fake.provider(t), Build{ID: "42", State: StateQueued}, testSHA, waitOptions(&fakeClock{}))
	if !errors.Is(err, ErrDeployTimeout) {
		t.Fatalf("persistent 503 must end at the deadline, got %v", err)
	}
}

func TestTeamCityRejectsBuildOfAnotherConfiguration(t *testing.T) {
	fake := newFakeTeamCity(t, map[string]any{"state": "finished", "status": "SUCCESS", "buildTypeId": "Other_Config",
		"revisions": map[string]any{"revision": []map[string]any{{"version": testSHA}}}})
	_, err := fake.provider(t).Build(context.Background(), "42")
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "another build configuration") {
		t.Fatalf("build of another configuration must be rejected, got %v", err)
	}
}

// gitRepo is a real local repository with a bare origin; gh is stubbed.
type gitRepo struct {
	t      *testing.T
	work   string
	remote string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	repo := &gitRepo{t: t, work: filepath.Join(root, "work"), remote: filepath.Join(root, "remote.git")}
	repo.git(root, "init", "-q", "--bare", "--initial-branch=main", repo.remote)
	repo.git(root, "init", "-q", "--initial-branch=main", repo.work)
	repo.git(repo.work, "remote", "add", "origin", repo.remote)
	repo.commitFile("base.txt", "base\n", "X")
	repo.git(repo.work, "push", "-q", "origin", "main")
	return repo
}

func (r *gitRepo) git(dir string, args ...string) string {
	r.t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	output, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (r *gitRepo) commitFile(name, content, message string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.work, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(r.work, "add", name)
	r.git(r.work, "commit", "-qm", message)
	return r.git(r.work, "rev-parse", "HEAD")
}

// prBranch creates the controller PR commit on a feature branch forked from
// main and returns to main.
func (r *gitRepo) prBranch(content string) string {
	r.t.Helper()
	r.git(r.work, "checkout", "-qb", "ai-team/feat", "main")
	head := r.commitFile("feature.txt", content, "P")
	r.git(r.work, "checkout", "-q", "main")
	return head
}

// ghStub answers gh pr view with a sequence of states and delegates git to a
// real runner.
type ghStub struct {
	mu     sync.Mutex
	views  []string
	calls  int
	runner delivery.CommandRunner
}

func (g *ghStub) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	if name != "gh" {
		return g.runner.Run(ctx, dir, name, args...)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	view := g.views[min(g.calls, len(g.views)-1)]
	g.calls++
	return delivery.StepResult{Status: delivery.StepPassed, Stdout: view}
}

func merged(oid string) string {
	return `{"state":"MERGED","baseRefName":"main","mergeCommit":{"oid":"` + oid + `"}}`
}

const openPR = `{"state":"OPEN","baseRefName":"main","mergeCommit":null}`

func (r *gitRepo) request(prHead string) MergeRequest {
	return MergeRequest{RepositoryDir: r.work, Remote: "origin", Branch: "main",
		PRURL: "https://github.example/org/repo/pull/1", PRHead: prHead}
}

func TestResolveMergedDeploySquashOnMovedMainDeploysBranchTip(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	repo.commitFile("y.txt", "Y\n", "Y")
	squash := repo.commitFile("feature.txt", "feature\n", "squash P")
	tip := repo.commitFile("z.txt", "Z\n", "Z")
	repo.git(repo.work, "push", "-q", "origin", "main")

	gh := &ghStub{views: []string{merged(squash)}, runner: EnvFilteredRunner{}}
	proof, err := ResolveMergedDeploy(context.Background(), gh, repo.request(prHead))
	if err != nil {
		t.Fatal(err)
	}
	if proof.MergeCommit != squash || proof.DeploySHA != tip {
		t.Fatalf("must deploy the branch tip %s with squash %s, got %+v (PR head %s)", tip, squash, proof, prHead)
	}

	// End to end: the build is pinned to the tip, never to the PR head that
	// lacks Y and Z.
	fake := newFakeTeamCity(t, finished("SUCCESS", tip))
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: gh,
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	build, err := Deploy(context.Background(), fake.provider(t), request)
	if err != nil || build.Revision != tip {
		t.Fatalf("deploy: %+v %v", build, err)
	}
	triggers, _ := fake.snapshot()
	encoded, _ := json.Marshal(triggers)
	if len(triggers) != 1 || !strings.Contains(string(encoded), tip) || strings.Contains(string(encoded), prHead) {
		t.Fatalf("trigger must pin the tip, got %s", encoded)
	}
}

func TestResolveMergedDeployMergeCommit(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	repo.commitFile("y.txt", "Y\n", "Y")
	repo.git(repo.work, "merge", "-q", "--no-ff", "-m", "merge PR", "ai-team/feat")
	mergeCommit := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "push", "-q", "origin", "main")
	proof, err := ResolveMergedDeploy(context.Background(), &ghStub{views: []string{merged(mergeCommit)}, runner: EnvFilteredRunner{}}, repo.request(prHead))
	if err != nil || proof.DeploySHA != mergeCommit {
		t.Fatalf("merge commit: %+v %v", proof, err)
	}
}

func TestResolveMergedDeployRejectsForeignOrDifferentMerge(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	altered := repo.commitFile("feature.txt", "something else\n", "squash with other content")
	repo.git(repo.work, "push", "-q", "origin", "main")
	for name, view := range map[string]string{
		"different squash content": merged(altered),
		"merge commit not on main": merged(prHead + ""),
		"closed":                   `{"state":"CLOSED","baseRefName":"main","mergeCommit":null}`,
		"other base":               `{"state":"MERGED","baseRefName":"release","mergeCommit":{"oid":"` + altered + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveMergedDeploy(context.Background(), &ghStub{views: []string{view}, runner: EnvFilteredRunner{}}, repo.request(prHead))
			if !errors.Is(err, ErrDeployRejected) {
				t.Fatalf("must be rejected, got %v", err)
			}
		})
	}
}

func TestDeployWaitsForMergeAndRejectsClosedPR(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	repo.git(repo.work, "merge", "-q", "--no-ff", "-m", "merge PR", "ai-team/feat")
	mergeCommit := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "push", "-q", "origin", "main")

	gh := &ghStub{views: []string{openPR, openPR, merged(mergeCommit)}, runner: EnvFilteredRunner{}}
	proof, err := WaitForMerge(context.Background(), gh, repo.request(prHead), waitOptions(&fakeClock{}))
	if err != nil || proof.DeploySHA != mergeCommit || gh.calls != 3 {
		t.Fatalf("must wait for the merge: %+v %v calls=%d", proof, err, gh.calls)
	}
	gh = &ghStub{views: []string{openPR}, runner: EnvFilteredRunner{}}
	if _, err := WaitForMerge(context.Background(), gh, repo.request(prHead), waitOptions(&fakeClock{})); !errors.Is(err, ErrDeployTimeout) {
		t.Fatalf("an open PR must time out, got %v", err)
	}

	fake := newFakeTeamCity(t, finished("SUCCESS", mergeCommit))
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead),
		Runner:      &ghStub{views: []string{`{"state":"CLOSED","baseRefName":"main","mergeCommit":null}`}, runner: EnvFilteredRunner{}},
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), MergeWait: waitOptions(&fakeClock{}), Wait: waitOptions(&fakeClock{})}
	if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("closed PR must be rejected, got %v", err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 0 {
		t.Fatalf("closed PR triggered %d builds", len(triggers))
	}
}

func mergedRepo(t *testing.T) (*gitRepo, string, string) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	repo.git(repo.work, "merge", "-q", "--no-ff", "-m", "merge PR", "ai-team/feat")
	mergeCommit := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "push", "-q", "origin", "main")
	return repo, prHead, mergeCommit
}

func TestDeployReReadsRecordedBuildInsteadOfTrustingReceipt(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	gh := &ghStub{views: []string{merged(tip)}, runner: EnvFilteredRunner{}}
	newRequest := func() DeployRequest {
		return DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: gh,
			ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	}
	tamper := func(request DeployRequest, runID string, build Build) {
		t.Helper()
		request.RunID = runID
		if err := writeReceipt(request, "teamcity", MergeProof{MergeCommit: tip, DeploySHA: tip}, build); err != nil {
			t.Fatal(err)
		}
	}
	forged := Build{ID: "42", BuildType: "Project_Deploy", State: StateFinished, Status: StatusSuccess,
		Revision: tip, WebURL: "https://evil.example/build/42"}

	// A forged "finished/success" receipt is re-read: the real build is still
	// running and the result link comes from TeamCity.
	fake := newFakeTeamCity(t, map[string]any{"state": "running"}, finished("SUCCESS", tip))
	request := newRequest()
	tamper(request, "run-1", forged)
	build, err := Deploy(context.Background(), fake.provider(t), request)
	if err != nil || strings.Contains(build.WebURL, "evil.example") || !strings.Contains(build.WebURL, fake.webHost) {
		t.Fatalf("receipt must not be trusted: %+v %v", build, err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 0 {
		t.Fatalf("recorded build must be resumed, got %d triggers", len(triggers))
	}
	receipt, _, _ := ReadReceipt(request.ReceiptPath)
	if receipt.Build.WebURL != build.WebURL || receipt.RunID != "run-1" || receipt.Stage != "deploy" {
		t.Fatalf("receipt not refreshed: %+v", receipt)
	}

	// A receipt pointing at a build of another configuration is rejected.
	other := newFakeTeamCity(t, map[string]any{"state": "finished", "status": "SUCCESS", "buildTypeId": "Other_Config",
		"revisions": map[string]any{"revision": []map[string]any{{"version": tip}}}})
	request = newRequest()
	tamper(request, "run-1", forged)
	if _, err := Deploy(context.Background(), other.provider(t), request); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("foreign build configuration must be rejected, got %v", err)
	}
	if triggers, _ := other.snapshot(); len(triggers) != 0 {
		t.Fatal("rejected receipt triggered a build")
	}

	// A receipt of another run is never reused.
	request = newRequest()
	tamper(request, "run-other", forged)
	if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("receipt of another run must be rejected, got %v", err)
	}

	// A genuine successful build is reused after re-reading, without a trigger.
	done := newFakeTeamCity(t, finished("SUCCESS", tip))
	request = newRequest()
	first, err := Deploy(context.Background(), done.provider(t), request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Deploy(context.Background(), done.provider(t), request)
	if err != nil || again.WebURL != first.WebURL {
		t.Fatalf("repeat deploy: %+v %v", again, err)
	}
	if triggers, _ := done.snapshot(); len(triggers) != 1 {
		t.Fatalf("repeat deploy must reuse the build, got %d triggers", len(triggers))
	}
	data, _ := os.ReadFile(request.ReceiptPath)
	if strings.Contains(string(data), testToken) {
		t.Fatal("receipt contains the token")
	}
}

func TestEnvFilteredRunnerDropsToken(t *testing.T) {
	if _, err := exec.LookPath("env"); err != nil {
		t.Skip("env is unavailable")
	}
	t.Setenv("AI_TEAM_TEST_TEAMCITY_TOKEN", testToken)
	t.Setenv("AI_TEAM_TEST_KEEP", "kept")
	result := EnvFilteredRunner{Drop: []string{"AI_TEAM_TEST_TEAMCITY_TOKEN"}}.Run(context.Background(), t.TempDir(), "env")
	if result.Status != delivery.StepPassed || strings.Contains(result.Stdout, testToken) || !strings.Contains(result.Stdout, "AI_TEAM_TEST_KEEP=kept") {
		t.Fatalf("token must not reach git/gh: status=%s", result.Status)
	}
}
