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
	build := map[string]any{"id": 42, "webUrl": "https://" + f.webHost + "/buildConfiguration/Deploy/42"}
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

// scriptedGit answers Git commands for VerifyMergedCommit without a repository.
type scriptedGit struct {
	mergeExit int
	resolved  string
	calls     [][]string
}

func (g *scriptedGit) Run(_ context.Context, _ string, name string, args ...string) delivery.StepResult {
	g.calls = append(g.calls, append([]string{name}, args...))
	result := delivery.StepResult{Status: delivery.StepPassed}
	switch args[0] {
	case "rev-parse":
		result.Stdout = g.resolved + "\n"
	case "merge-base":
		if g.mergeExit != 0 {
			result = delivery.StepResult{Status: delivery.StepFailed, ExitCode: g.mergeExit, Stderr: "https://" + testToken + "@git.example"}
		}
	}
	return result
}

func deployRequest(t *testing.T, runner delivery.CommandRunner, clock Clock) DeployRequest {
	t.Helper()
	return DeployRequest{
		RepositoryDir: t.TempDir(), Remote: "origin", Branch: "main", CommitSHA: testSHA,
		ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Runner: runner, Wait: waitOptions(clock),
	}
}

func TestDeployRefusesUnmergedCommitBeforeTrigger(t *testing.T) {
	fake := newFakeTeamCity(t, finished("SUCCESS", testSHA))
	git := &scriptedGit{mergeExit: 1, resolved: testSHA}
	_, err := Deploy(context.Background(), fake.provider(t), deployRequest(t, git, &fakeClock{}))
	if !errors.Is(err, ErrDeployRejected) || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("unmerged commit must be rejected, got %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("git diagnostics leaked into error: %v", err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 0 {
		t.Fatalf("unmerged commit triggered %d builds", len(triggers))
	}

	git = &scriptedGit{resolved: otherSHA}
	if _, err := Deploy(context.Background(), fake.provider(t), deployRequest(t, git, &fakeClock{})); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("commit resolving to another object must be rejected, got %v", err)
	}
	request := deployRequest(t, &scriptedGit{resolved: testSHA}, &fakeClock{})
	request.CommitSHA = "HEAD"
	if _, err := Deploy(context.Background(), fake.provider(t), request); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("symbolic revision must be rejected, got %v", err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 0 {
		t.Fatalf("invalid commits triggered %d builds", len(triggers))
	}
}

func TestDeployReusesAndResumesRecordedBuild(t *testing.T) {
	fake := newFakeTeamCity(t, map[string]any{"state": "running"}, finished("SUCCESS", testSHA))
	request := deployRequest(t, &scriptedGit{resolved: testSHA}, &fakeClock{})
	first, err := Deploy(context.Background(), fake.provider(t), request)
	if err != nil || first.Status != StatusSuccess {
		t.Fatalf("first deploy: %+v %v", first, err)
	}
	again, err := Deploy(context.Background(), fake.provider(t), request)
	if err != nil || again.WebURL != first.WebURL {
		t.Fatalf("repeat deploy: %+v %v", again, err)
	}
	if triggers, _ := fake.snapshot(); len(triggers) != 1 {
		t.Fatalf("repeat deploy must reuse the successful build, got %d triggers", len(triggers))
	}

	pending := newFakeTeamCity(t, map[string]any{"state": "running"}, finished("SUCCESS", testSHA))
	pendingRequest := deployRequest(t, &scriptedGit{resolved: testSHA}, &fakeClock{})
	if err := writeReceipt(pendingRequest.ReceiptPath, "teamcity", pendingRequest, Build{ID: "42", State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	resumed, err := Deploy(context.Background(), pending.provider(t), pendingRequest)
	if err != nil || resumed.Status != StatusSuccess {
		t.Fatalf("resumed deploy: %+v %v", resumed, err)
	}
	if triggers, _ := pending.snapshot(); len(triggers) != 0 {
		t.Fatalf("resume must poll the recorded build instead of triggering, got %d triggers", len(triggers))
	}
	receipt, found, err := ReadReceipt(pendingRequest.ReceiptPath)
	if err != nil || !found || receipt.Build.Status != StatusSuccess || receipt.CommitSHA != testSHA {
		t.Fatalf("final receipt: %+v found=%v err=%v", receipt, found, err)
	}
	data, _ := os.ReadFile(pendingRequest.ReceiptPath)
	if strings.Contains(string(data), testToken) {
		t.Fatal("receipt contains the token")
	}
}

func TestVerifyMergedCommitWithRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	run := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run(root, "init", "--bare", "--initial-branch=main", remote)
	run(root, "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "commit", "-qm", "merged")
	merged := run(work, "rev-parse", "HEAD")
	run(work, "remote", "add", "origin", remote)
	run(work, "push", "-q", "origin", "main")
	run(work, "checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "commit", "-qm", "unmerged")
	unmerged := run(work, "rev-parse", "HEAD")

	if err := VerifyMergedCommit(context.Background(), nil, work, "origin", "main", merged); err != nil {
		t.Fatalf("merged commit: %v", err)
	}
	if err := VerifyMergedCommit(context.Background(), nil, work, "origin", "main", unmerged); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("unmerged commit must be rejected, got %v", err)
	}
	if err := VerifyMergedCommit(context.Background(), nil, work, "origin", "main", strings.Repeat("a", 40)); !errors.Is(err, ErrDeployRejected) {
		t.Fatalf("unknown commit must be rejected, got %v", err)
	}
	for _, branch := range []string{"-main", "main..x", "a b", "main/"} {
		if err := VerifyMergedCommit(context.Background(), nil, work, "origin", branch, merged); !errors.Is(err, ErrDeployRejected) {
			t.Fatalf("branch %q must be rejected, got %v", branch, err)
		}
	}
}
