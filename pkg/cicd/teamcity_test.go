package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
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
	// triggerMode: "" queues, "drop" closes the connection without an answer,
	// "reject" answers HTTP 400.
	triggerMode string
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
		switch f.triggerMode {
		case "drop":
			if hijacker, ok := w.(http.Hijacker); ok {
				if connection, _, err := hijacker.Hijack(); err == nil {
					_ = connection.Close()
					return
				}
			}
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		case "reject":
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		case "garbage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":42,"state":`))
			return
		case "noid":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"state":"queued"}`))
			return
		}
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
	// GitHub keeps refs/pull/<n>/head; the deploy proof fetches it.
	r.git(r.work, "push", "-q", "origin", "ai-team/feat:refs/pull/1/head")
	r.git(r.work, "checkout", "-q", "main")
	return head
}

// ghStub answers gh pr view with a sequence of states and delegates git to a
// real runner.
type ghStub struct {
	mu     sync.Mutex
	views  []string
	calls  int
	head   string
	remote string
	runner delivery.CommandRunner
}

func (g *ghStub) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	if name != "gh" {
		return g.runner.Run(ctx, dir, name, args...)
	}
	if args[0] == "api" {
		tip := g.runner.Run(ctx, "", "git", "--git-dir", g.remote, "rev-parse", "refs/heads/main")
		return delivery.StepResult{Status: delivery.StepPassed,
			Stdout: `{"name":"main","commit":{"sha":"` + strings.TrimSpace(tip.Stdout) + `"}}`}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	view := g.views[min(g.calls, len(g.views)-1)]
	g.calls++
	return delivery.StepResult{Status: delivery.StepPassed, Stdout: strings.ReplaceAll(view, "__HEAD__", g.head)}
}

func merged(oid string) string {
	return `{"state":"MERGED","baseRefName":"main","headRefOid":"__HEAD__","mergeCommit":{"oid":"` + oid + `"}}`
}

const (
	openPR   = `{"state":"OPEN","baseRefName":"main","headRefOid":"__HEAD__","mergeCommit":null}`
	closedPR = `{"state":"CLOSED","baseRefName":"main","headRefOid":"__HEAD__","mergeCommit":null}`
)

// gh builds a gh stub for this repository: pr view answers come from views
// (with the PR head substituted), the branch API reports the bare remote tip.
func (r *gitRepo) gh(prHead string, views ...string) *ghStub {
	return &ghStub{views: views, head: prHead, remote: r.remote, runner: EnvFilteredRunner{}}
}

func (r *gitRepo) request(prHead string) MergeRequest {
	return MergeRequest{Repository: "github.example/org/repo", FetchURL: r.remote, Branch: "main",
		PRURL: "https://github.example/org/repo/pull/1", PRHead: prHead}
}

func TestResolveMergedDeploySquashOnMovedMainDeploysBranchTip(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	repo.commitFile("y.txt", "Y\n", "Y")
	squash := repo.commitFile("feature.txt", "feature\n", "squash P")
	tip := repo.commitFile("z.txt", "Z\n", "Z")
	repo.git(repo.work, "push", "-q", "origin", "main")

	gh := repo.gh(prHead, merged(squash))
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
	proof, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(mergeCommit)), repo.request(prHead))
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
		"closed":                   closedPR,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, view), repo.request(prHead))
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

	gh := repo.gh(prHead, openPR, openPR, merged(mergeCommit))
	proof, err := WaitForMerge(context.Background(), gh, repo.request(prHead), waitOptions(&fakeClock{}))
	if err != nil || proof.DeploySHA != mergeCommit || gh.calls != 3 {
		t.Fatalf("must wait for the merge: %+v %v calls=%d", proof, err, gh.calls)
	}
	gh = repo.gh(prHead, openPR)
	if _, err := WaitForMerge(context.Background(), gh, repo.request(prHead), waitOptions(&fakeClock{})); !errors.Is(err, ErrDeployTimeout) {
		t.Fatalf("an open PR must time out, got %v", err)
	}

	fake := newFakeTeamCity(t, finished("SUCCESS", mergeCommit))
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead),
		Runner:      repo.gh(prHead, closedPR),
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
	gh := repo.gh(prHead, merged(tip))
	newRequest := func() DeployRequest {
		return DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: gh,
			ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	}
	tamper := func(request DeployRequest, runID string, build Build) {
		t.Helper()
		request.RunID = runID
		if err := writeReceipt(request, "teamcity", MergeProof{MergeCommit: tip, DeploySHA: tip}, build, false); err != nil {
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

func TestResolveMergedDeployRejectsOtherBaseAndForeignHead(t *testing.T) {
	repo, prHead, mergeCommit := mergedRepo(t)
	otherBase := `{"state":"MERGED","baseRefName":"release","headRefOid":"__HEAD__","mergeCommit":{"oid":"` + mergeCommit + `"}}`
	if _, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, otherBase), repo.request(prHead)); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "release") {
		t.Fatalf("valid merge into another base must be rejected, got %v", err)
	}
	stub := repo.gh(prHead, merged(mergeCommit))
	stub.head = strings.Repeat("b", 40)
	if _, err := ResolveMergedDeploy(context.Background(), stub, repo.request(prHead)); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "published by the controller") {
		t.Fatalf("PR head other than the controller commit must be rejected, got %v", err)
	}
	request := repo.request(prHead)
	request.PRURL = "https://github.example/other/repo/pull/1"
	if _, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(mergeCommit)), request); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("PR of another repository must be rejected, got %v", err)
	}
}

// TestResolveMergedDeployIgnoresInsteadOfRedirect: a checkout-local
// url.<evil>.insteadOf rewrite must not change the deployed tip.
func TestResolveMergedDeployIgnoresInsteadOfRedirect(t *testing.T) {
	repo, prHead, mergeCommit := mergedRepo(t)
	evil := filepath.Join(t.TempDir(), "evil.git")
	repo.git(filepath.Dir(evil), "clone", "-q", "--bare", repo.remote, evil)
	scratch := filepath.Join(t.TempDir(), "scratch")
	repo.git(filepath.Dir(scratch), "clone", "-q", evil, scratch)
	if err := os.WriteFile(filepath.Join(scratch, "backdoor.txt"), []byte("evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.git(scratch, "add", ".")
	repo.git(scratch, "-c", "user.name=e", "-c", "user.email=e@example.test", "commit", "-qm", "evil")
	evilTip := repo.git(scratch, "rev-parse", "HEAD")
	repo.git(scratch, "push", "-q", "origin", "main")
	repo.git(repo.work, "config", "url."+evil+".insteadOf", repo.remote)
	repo.git(repo.work, "config", "remote.origin.url", repo.remote)

	proof, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(mergeCommit)), repo.request(prHead))
	if err != nil {
		t.Fatal(err)
	}
	if proof.DeploySHA == evilTip || proof.DeploySHA != mergeCommit {
		t.Fatalf("insteadOf redirect changed the deployed tip: %+v (evil %s, real %s)", proof, evilTip, mergeCommit)
	}
}

// TestResolveMergedDeployIgnoresTextconv: a textconv driver from checkout
// attributes must not hide extra content in a squash commit.
func TestResolveMergedDeployIgnoresTextconv(t *testing.T) {
	repo := newGitRepo(t)
	repo.commitFile("payload.bin", "v1\n", "payload v1")
	repo.git(repo.work, "push", "-q", "origin", "main")
	prHead := repo.prBranch("feature\n")
	if err := os.WriteFile(filepath.Join(repo.work, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The textconv driver maps both payload versions to the same (empty) text,
	// which hides this modification from a textconv diff.
	if err := os.WriteFile(filepath.Join(repo.work, "payload.bin"), []byte("hidden payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.git(repo.work, "add", ".")
	repo.git(repo.work, "commit", "-qm", "squash P with hidden payload")
	squash := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(repo.work, ".git", "info", "attributes"), []byte("payload.bin diff=hide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.git(repo.work, "config", "diff.hide.textconv", "true")

	_, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(squash)), repo.request(prHead))
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "exactly the pull request change") {
		t.Fatalf("squash with hidden extra content must be rejected, got %v", err)
	}
}

func TestDeployKeepsPendingTriggerWhenOutcomeIsUnknown(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	fake := newFakeTeamCity(t, finished("SUCCESS", tip))
	fake.triggerMode = "drop"
	request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: repo.gh(prHead, merged(tip)),
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
	if _, err := Deploy(context.Background(), fake.provider(t), request); err == nil || IsTransient(err) {
		t.Fatalf("lost trigger response must fail without a retryable error, got %v", err)
	}
	receipt, found, err := ReadReceipt(request.ReceiptPath)
	if err != nil || !found || !receipt.TriggerPending || receipt.Build.ID != "" {
		t.Fatalf("pending trigger marker missing: %+v %v %v", receipt, found, err)
	}
	fake.mu.Lock()
	fake.triggerMode = ""
	fake.mu.Unlock()
	if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("retry after an unknown trigger outcome must be refused, got %v", err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 1 {
		t.Fatalf("retry must not trigger a second build, got %d", len(triggers))
	}

	rejected := newFakeTeamCity(t, finished("SUCCESS", tip))
	rejected.triggerMode = "reject"
	request.ReceiptPath = filepath.Join(t.TempDir(), "receipt.json")
	if _, err := Deploy(context.Background(), rejected.provider(t), request); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("expected rejected trigger, got %v", err)
	}
	rejected.mu.Lock()
	rejected.triggerMode = ""
	rejected.mu.Unlock()
	if build, err := Deploy(context.Background(), rejected.provider(t), request); err != nil || build.Status != StatusSuccess {
		t.Fatalf("a definite trigger rejection must allow a retry: %+v %v", build, err)
	}
}

func TestDeployKeepsPendingTriggerAfterUnreadableAcceptedResponse(t *testing.T) {
	for _, mode := range []string{"garbage", "noid"} {
		t.Run(mode, func(t *testing.T) {
			repo, prHead, tip := mergedRepo(t)
			fake := newFakeTeamCity(t, finished("SUCCESS", tip))
			fake.triggerMode = mode
			request := DeployRequest{RunID: "run-1", Stage: "deploy", Merge: repo.request(prHead), Runner: repo.gh(prHead, merged(tip)),
				ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Wait: waitOptions(&fakeClock{})}
			if _, err := Deploy(context.Background(), fake.provider(t), request); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
				t.Fatalf("2xx with unreadable body must leave the outcome unknown, got %v", err)
			}
			fake.mu.Lock()
			fake.triggerMode = ""
			fake.mu.Unlock()
			if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "unknown outcome") {
				t.Fatalf("retry must be refused, got %v", err)
			}
			if triggers, _ := fake.snapshot(); len(triggers) != 1 {
				t.Fatalf("an accepted trigger must never be repeated, got %d", len(triggers))
			}
		})
	}
	for status, definite := range map[int]bool{400: true, 403: true, 404: true, 408: false, 429: false, 500: false, 503: false} {
		err := fmt.Errorf("teamcity %w", &HTTPStatusError{Method: "POST", Path: "/app/rest/buildQueue", StatusCode: status})
		if IsDefiniteRejection(err) != definite {
			t.Fatalf("HTTP %d definite=%v", status, !definite)
		}
	}
	if IsDefiniteRejection(errors.New("decode teamcity response: invalid JSON")) {
		t.Fatal("an unreadable 2xx answer is not a rejection")
	}
}

func TestIsolatedGitEnvironment(t *testing.T) {
	env := strings.Join(isolatedGitEnv("/tmp/x.git", "!helper"), "\n")
	for _, want := range []string{
		"GIT_DIR=/tmp/x.git", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull,
		"GIT_CONFIG_KEY_0=credential.helper\nGIT_CONFIG_VALUE_0=\n",
		"GIT_CONFIG_KEY_1=credential.helper\nGIT_CONFIG_VALUE_1=!helper",
		"=core.commitGraph\n", "core.hooksPath",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("isolated git environment lacks %q:\n%s", want, env)
		}
	}
	if !strings.Contains(strings.Join(diffArgs, " "), "--ignore-submodules=none") || !strings.Contains(strings.Join(diffArgs, " "), "--no-textconv") {
		t.Fatalf("diff options are not pinned: %v", diffArgs)
	}
}

// newAuthGitServer serves bare repositories under root through git
// http-backend behind HTTP basic auth, like a private GitHub repository.
func newAuthGitServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git exec path is unavailable")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git http-backend is unavailable")
	}
	handler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "deploy" || password != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestResolveMergedDeployPrivateRepositoryUsesExplicitHelperOnly(t *testing.T) {
	repo, prHead, tip := mergedRepo(t)
	server := newAuthGitServer(t, filepath.Dir(repo.remote))
	marker := filepath.Join(t.TempDir(), "foreign-helper-ran")
	// A helper in the controller's global Git config and one in the checkout
	// must never run: only the explicitly passed helper may answer.
	global := filepath.Join(t.TempDir(), "gitconfig")
	foreign := "!f() { touch " + marker + "; echo username=deploy; echo password=s3cret; }; f"
	if err := os.WriteFile(global, []byte("[credential]\n\thelper = \""+foreign+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	repo.git(repo.work, "config", "credential.helper", foreign)

	request := repo.request(prHead)
	request.FetchURL = server.URL + "/" + filepath.Base(repo.remote)
	request.CredentialHelper = "!f() { echo username=deploy; echo password=s3cret; }; f"
	proof, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(tip)), request)
	if err != nil || proof.DeploySHA != tip {
		t.Fatalf("private fetch with the explicit helper: %+v %v", proof, err)
	}
	request.CredentialHelper = "!true"
	if _, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(tip)), request); err == nil || !IsTransient(err) {
		t.Fatalf("fetch without credentials must fail, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a credential helper from the global or checkout config was executed")
	}
}

func TestResolveMergedDeployIgnoresCheckoutReplaceAndSubmoduleConfig(t *testing.T) {
	repo := newGitRepo(t)
	prHead := repo.prBranch("feature\n")
	tip := repo.commitFile("y.txt", "Y\n", "Y")
	repo.git(repo.work, "push", "-q", "origin", "main")
	// In the checkout, graft prHead into main's history: with replace refs
	// honored, the PR head would look merged.
	repo.git(repo.work, "replace", "--graft", tip, tip+"^", prHead)
	if _, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(prHead)), repo.request(prHead)); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("checkout replace refs must not prove a merge, got %v", err)
	}

	// A squash that also moves a submodule pointer must be rejected even if
	// the checkout ignores submodules in diffs.
	repo.git(repo.work, "config", "diff.ignoreSubmodules", "all")
	if err := os.WriteFile(filepath.Join(repo.work, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.git(repo.work, "add", "feature.txt")
	repo.git(repo.work, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("c", 40)+",vendor/lib")
	repo.git(repo.work, "commit", "-qm", "squash P with submodule bump")
	squash := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "push", "-q", "origin", "main")
	_, err := ResolveMergedDeploy(context.Background(), repo.gh(prHead, merged(squash)), repo.request(prHead))
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "exactly the pull request change") {
		t.Fatalf("squash with an extra submodule change must be rejected, got %v", err)
	}
}
