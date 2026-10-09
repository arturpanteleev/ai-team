//go:build linux || darwin

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

type questionAnswerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f questionAnswerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestWorkerAPIQuestionAnswerProviderBindsResponseToRequestAndReadOnlyFile(t *testing.T) {
	target := t.TempDir()
	const runID, approvalID = "question-provider-run", "approval-provider"
	expected, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(expected), 0700); err != nil {
		t.Fatal(err)
	}
	answer := []byte("# Ответ Product Owner\n\nB2B buyers\n")
	if err := os.WriteFile(expected, answer, 0444); err != nil {
		t.Fatal(err)
	}

	requestCount := 0
	port := &workerAPIPort{
		address: "http://unix", token: "controller-token", socketPath: "/controller.sock",
		scope: workerAPIScope{RunID: runID, Operation: OperationRecover, TargetDir: target},
		client: &http.Client{Transport: questionAnswerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requestCount++
			if request.Method != http.MethodPost || request.URL.Path != "/v1/call" ||
				request.Header.Get("Authorization") != "Bearer controller-token" {
				t.Errorf("unexpected controller request: method=%q path=%q auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
			}
			var envelope workerAPIRequest
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				t.Errorf("decode controller request: %v", err)
			}
			var call workerAPICall
			if err := json.Unmarshal(envelope.Payload, &call); err != nil || envelope.Method != "handoff.question_answer.path" ||
				envelope.RunID != runID || call.RunID != runID || call.A != approvalID {
				t.Errorf("request must contain only scoped run and approval identity: envelope=%+v call=%+v err=%v", envelope, call, err)
			}
			body, _ := json.Marshal(expected)
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(string(body))), Request: request,
			}, nil
		})},
	}
	provider := NewWorkerAPIQuestionAnswerInputs(port)
	artifact, err := provider.MaterializeQuestionAnswer(runID, approvalID)
	if err != nil || requestCount != 1 {
		t.Fatalf("controller-backed input request failed: artifact=%+v calls=%d err=%v", artifact, requestCount, err)
	}
	if artifact.Name != "clarification-answer" || artifact.Path != expected || artifact.Size != int64(len(answer)) || artifact.ModTime.IsZero() {
		t.Fatalf("provider returned incorrect artifact identity: %+v", artifact)
	}

	var nilProvider *workerAPIQuestionAnswerInputs
	if _, err := nilProvider.MaterializeQuestionAnswer(runID, approvalID); err == nil {
		t.Fatal("nil provider was accepted")
	}
	if _, err := (&workerAPIQuestionAnswerInputs{}).MaterializeQuestionAnswer(runID, approvalID); err == nil {
		t.Fatal("provider without an API port was accepted")
	}
	unsupported := *port
	unsupported.address = "http://127.0.0.1:1234"
	if _, err := NewWorkerAPIQuestionAnswerInputs(&unsupported).MaterializeQuestionAnswer(runID, approvalID); err == nil {
		t.Fatal("non-sandboxed controller API transport was accepted")
	}
	if _, err := provider.MaterializeQuestionAnswer("another-run", approvalID); err == nil {
		t.Fatal("cross-run input request was accepted")
	}
	if _, err := provider.MaterializeQuestionAnswer(runID, "bad/id"); err == nil {
		t.Fatal("invalid approval path component was accepted")
	}
}

func TestWorkerAPIQuestionAnswerProviderRejectsUntrustedControllerPathsAndFiles(t *testing.T) {
	const runID, approvalID = "question-provider-reject-run", "approval-provider-reject"
	tests := []struct {
		name       string
		setup      func(string) error
		response   func(string) string
		wantNoCall bool
	}{
		{name: "unexpected controller path", response: func(path string) string { return path + ".other" }},
		{name: "missing materialization", response: func(path string) string { return path }},
		{name: "symlink materialization", response: func(path string) string { return path }, setup: func(path string) error {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(path+".target", []byte("answer"), 0444); err != nil {
				return err
			}
			return os.Symlink(path+".target", path)
		}},
		{name: "directory materialization", response: func(path string) string { return path }, setup: func(path string) error {
			return os.MkdirAll(path, 0700)
		}},
		{name: "writable materialization", response: func(path string) string { return path }, setup: func(path string) error {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("answer"), 0644)
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			target := t.TempDir()
			expected, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.setup != nil {
				if err := testCase.setup(expected); err != nil {
					t.Fatal(err)
				}
			}
			requestCount := 0
			responsePath := testCase.response(expected)
			port := &workerAPIPort{
				address: "http://unix", token: "token", socketPath: "/controller.sock",
				scope: workerAPIScope{RunID: runID, Operation: OperationRecover, TargetDir: target},
				client: &http.Client{Transport: questionAnswerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					requestCount++
					body, _ := json.Marshal(responsePath)
					return &http.Response{
						StatusCode: http.StatusOK, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader(string(body))), Request: request,
					}, nil
				})},
			}
			_, err = NewWorkerAPIQuestionAnswerInputs(port).MaterializeQuestionAnswer(runID, approvalID)
			if err == nil || requestCount != 1 {
				t.Fatalf("unsafe controller path/file was accepted or not requested: calls=%d err=%v", requestCount, err)
			}
		})
	}
}

func TestWorkerAPIQuestionAnswerProviderPropagatesControllerFailure(t *testing.T) {
	target := t.TempDir()
	const runID, approvalID = "question-provider-error", "approval-provider-error"
	transportErr := errors.New("controller unavailable")
	port := &workerAPIPort{
		address: "http://unix", token: "token", socketPath: "/controller.sock",
		scope: workerAPIScope{RunID: runID, Operation: OperationRecover, TargetDir: target},
		client: &http.Client{Transport: questionAnswerRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
	}
	if _, err := NewWorkerAPIQuestionAnswerInputs(port).MaterializeQuestionAnswer(runID, approvalID); !errors.Is(err, transportErr) {
		t.Fatalf("controller transport failure should reach the caller unchanged: %v", err)
	}

	invalidScope := *port
	invalidScope.scope.RunID = "../unsafe-run"
	if _, err := NewWorkerAPIQuestionAnswerInputs(&invalidScope).MaterializeQuestionAnswer("../unsafe-run", approvalID); err == nil {
		t.Fatal("unsafe run identity was sent to the controller")
	}
}

func TestValidateQuestionAnswerCandidateUsesCurrentWorkspaceIdentity(t *testing.T) {
	target := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "tests@example.com"}, {"config", "user.name", "Coverage Test"}} {
		command := exec.Command("git", append([]string{"-C", target}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("candidate baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "candidate baseline"}} {
		command := exec.Command("git", append([]string{"-C", target}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	const runID = "question-answer-candidate"
	manager, available, err := candidate.Create(context.Background(), target, runID)
	if err != nil || !available || manager == nil {
		t.Fatalf("create candidate fixture: available=%v manager=%v err=%v", available, manager, err)
	}
	identity, err := manager.Identity()
	if err != nil {
		t.Fatal(err)
	}
	value := approval.PendingApproval{RunID: runID, CandidateSHA256: identity.WorkspaceSHA256}
	if err := validateQuestionAnswerCandidate(context.TODO(), target, value); err != nil {
		t.Fatalf("matching controller candidate identity should be admitted: %v", err)
	}
	if err := validateQuestionAnswerCandidate(context.Background(), target, approval.PendingApproval{RunID: runID}); err != nil {
		t.Fatalf("approval without a candidate binding should not require a worktree: %v", err)
	}

	changed := value
	changed.CandidateSHA256 = strings.Repeat("0", 64)
	if err := validateQuestionAnswerCandidate(context.Background(), target, changed); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed workspace identity was accepted: %v", err)
	}
	missing := value
	missing.RunID = "candidate-metadata-missing"
	if err := validateQuestionAnswerCandidate(context.Background(), target, missing); err == nil || !strings.Contains(err.Error(), "load clarification candidate identity") {
		t.Fatalf("missing candidate metadata did not fail closed: %v", err)
	}
}

func TestValidateQuestionAnswerMountChecksParentsProjectionAndIdentity(t *testing.T) {
	const runID, approvalID = "question-mount-validation", "approval-mount-validation"
	newFixture := func(t *testing.T) (string, string, string, workerReadOnlyInputMount) {
		t.Helper()
		target := t.TempDir()
		value := workerAnalystQuestionApproval(runID, approvalID, "Who is the buyer?", "B2B buyers", approval.StatusResolved)
		store := pipeline.ControllerQuestionAnswerStore{TargetDir: target}
		source, err := store.Prepare(value)
		if err != nil {
			t.Fatal(err)
		}
		destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, nil, 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(destination)
		if err != nil {
			t.Fatal(err)
		}
		return target, source, destination, workerReadOnlyInputMount{SourcePath: source, TargetPath: destination, TargetInfo: info}
	}

	t.Run("empty target mountpoint is valid", func(t *testing.T) {
		target, _, _, mount := newFixture(t)
		if err := validateQuestionAnswerMount(target, runID, mount); err != nil {
			t.Fatalf("controller-created empty mountpoint should be valid: %v", err)
		}
	})
	t.Run("empty paths are rejected", func(t *testing.T) {
		if err := validateQuestionAnswerMount(t.TempDir(), runID, workerReadOnlyInputMount{}); err == nil {
			t.Fatal("mount without paths was accepted")
		}
	})
	t.Run("wrong target identity is rejected", func(t *testing.T) {
		target, _, _, mount := newFixture(t)
		mount.TargetPath += ".other"
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil {
			t.Fatal("cross-approval mount target was accepted")
		}
	})
	t.Run("source must remain read only", func(t *testing.T) {
		target, source, _, mount := newFixture(t)
		if err := os.Chmod(source, 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("writable canonical answer was accepted: %v", err)
		}
	})
	t.Run("source must remain nonempty", func(t *testing.T) {
		target, source, _, mount := newFixture(t)
		if err := os.Chmod(source, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(source, 0444); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "nonempty read-only regular file") {
			t.Fatalf("empty canonical answer was accepted: %v", err)
		}
	})
	t.Run("missing source file is rejected", func(t *testing.T) {
		target, source, _, mount := newFixture(t)
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "read canonical clarification input safely") {
			t.Fatalf("missing canonical answer was accepted: %v", err)
		}
	})
	t.Run("missing target input parent is rejected", func(t *testing.T) {
		target, _, destination, mount := newFixture(t)
		if err := os.RemoveAll(filepath.Dir(destination)); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "mountpoint parents") {
			t.Fatalf("missing worker input parent was accepted: %v", err)
		}
	})
	t.Run("target identity is pinned", func(t *testing.T) {
		target, _, destination, mount := newFixture(t)
		canonical, err := pipeline.CanonicalQuestionAnswerContent(workerAnalystQuestionApproval(
			runID, approvalID, "Who is the buyer?", "B2B buyers", approval.StatusResolved))
		if err != nil {
			t.Fatal(err)
		}
		replacement := destination + ".replacement"
		if err := os.WriteFile(replacement, canonical, 0444); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, destination); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "identity changed") {
			t.Fatalf("replacement target file was accepted: %v", err)
		}
	})
	t.Run("missing target file is rejected", func(t *testing.T) {
		target, _, destination, mount := newFixture(t)
		if err := os.Remove(destination); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); !os.IsNotExist(err) {
			t.Fatalf("missing target mountpoint should preserve its filesystem error, got %v", err)
		}
	})
	t.Run("unsafe run identity is rejected", func(t *testing.T) {
		target, _, _, mount := newFixture(t)
		if err := validateQuestionAnswerMount(target, "../unsafe-run", mount); err == nil {
			t.Fatal("mount for an unsafe run id was accepted")
		}
	})
	t.Run("target bytes must match canonical answer", func(t *testing.T) {
		target, _, destination, mount := newFixture(t)
		mount.TargetInfo = nil
		if err := os.WriteFile(destination, []byte("different answer"), 0444); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "exact read-only canonical overlay") {
			t.Fatalf("different worker-visible answer was accepted: %v", err)
		}
	})
	t.Run("target overlay must remain read only", func(t *testing.T) {
		target, _, destination, mount := newFixture(t)
		canonical, err := pipeline.CanonicalQuestionAnswerContent(workerAnalystQuestionApproval(
			runID, approvalID, "Who is the buyer?", "B2B buyers", approval.StatusResolved))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, canonical, 0644); err != nil {
			t.Fatal(err)
		}
		if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "exact read-only canonical overlay") {
			t.Fatalf("writable answer overlay was accepted: %v", err)
		}
	})
}

func TestValidateQuestionAnswerMountRejectsMissingParentsAndSpecialFiles(t *testing.T) {
	const runID, approvalID = "question-mount-errors", "approval-mount-errors"
	target := t.TempDir()
	value := workerAnalystQuestionApproval(runID, approvalID, "Who is the buyer?", "B2B buyers", approval.StatusResolved)
	store := pipeline.ControllerQuestionAnswerStore{TargetDir: target}
	source, err := store.Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mount := workerReadOnlyInputMount{SourcePath: source, TargetPath: destination}
	if err := os.RemoveAll(filepath.Dir(source)); err != nil {
		t.Fatal(err)
	}
	if err := validateQuestionAnswerMount(target, runID, mount); err == nil || !strings.Contains(err.Error(), "canonical clarification input parents") {
		t.Fatalf("missing canonical parent directory was accepted: %v", err)
	}

	if _, _, err := readQuestionAnswerMountFile(filepath.Join(target, "missing-answer"), maxQuestionAnswerMountBytes); !os.IsNotExist(err) {
		t.Fatalf("missing mount input should report its filesystem error, got %v", err)
	}
	large := filepath.Join(target, "large-answer")
	if err := os.WriteFile(large, make([]byte, maxQuestionAnswerMountBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readQuestionAnswerMountFile(large, maxQuestionAnswerMountBytes); err == nil || !strings.Contains(err.Error(), "bounded regular file") {
		t.Fatalf("oversized clarification input was accepted: %v", err)
	}
	if _, err := questionAnswerMountFileLinkCount(nil); err == nil {
		t.Fatal("link count accepted a missing file handle")
	}
	closed, err := os.Open(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := questionAnswerMountFileLinkCount(closed); err == nil {
		t.Fatal("link count accepted a closed file descriptor")
	}
	if _, err := openQuestionAnswerMountFileNoFollow(large + ".missing"); !os.IsNotExist(err) {
		t.Fatalf("no-follow open should preserve the filesystem error, got %v", err)
	}
	symlink := filepath.Join(t.TempDir(), "answer-symlink")
	if err := os.Symlink(large, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := openQuestionAnswerMountFileNoFollow(symlink); err == nil {
		t.Fatal("no-follow open followed a leaf symlink")
	}
}

func TestCleanupQuestionAnswerMountpointRemovesOnlyOriginalEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answer.md")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &workerAPIServer{questionAnswerPath: path, questionAnswerMountInfo: info, questionAnswerMountCreated: true}
	server.cleanupQuestionAnswerMountpoint()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("controller-created empty mountpoint should be cleaned up, got %v", err)
	}

	for _, testCase := range []struct {
		name  string
		write []byte
	}{
		{name: "nonempty file", write: []byte("answer")},
		{name: "replacement file", write: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "answer.md")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			original, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.name == "replacement file" {
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, testCase.write, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, testCase.write, 0600); err != nil {
				t.Fatal(err)
			}
			server := &workerAPIServer{questionAnswerPath: path, questionAnswerMountInfo: original, questionAnswerMountCreated: true}
			server.cleanupQuestionAnswerMountpoint()
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("cleanup removed a non-owned mountpoint: %v", err)
			}
		})
	}
	var nilServer *workerAPIServer
	nilServer.cleanupQuestionAnswerMountpoint()
}

type questionAnswerLifecyclePort struct {
	state lifecycle.State
	err   error
}

func (s questionAnswerLifecyclePort) Create(lifecycle.State) error { return nil }
func (s questionAnswerLifecyclePort) Load(string) (lifecycle.State, error) {
	return s.state, s.err
}
func (s questionAnswerLifecyclePort) Save(lifecycle.State, lifecycle.State) error { return nil }

func TestPrepareQuestionAnswerMountSkipsUnsupportedControllerStates(t *testing.T) {
	if mount, err := (*workerAPIServer)(nil).prepareQuestionAnswerMount(context.Background()); mount != nil || err != nil {
		t.Fatalf("nil server should skip clarification admission: mount=%+v err=%v", mount, err)
	}

	target := t.TempDir()
	const runID = "question-admission-guards"
	newServer := func(operation Operation, port questionAnswerLifecyclePort) *workerAPIServer {
		return &workerAPIServer{
			scope:        workerAPIScope{RunID: runID, Operation: operation, TargetDir: target},
			usageAllowed: true, eventLogs: evidence.ControllerEventStore{TargetDir: target},
			lifecycle: port, approvals: &apiApprovalStore{},
			questionAnswerStore: pipeline.ControllerQuestionAnswerStore{TargetDir: target},
		}
	}

	tests := []struct {
		name      string
		server    *workerAPIServer
		wantError string
	}{
		{name: "missing controller capability", server: &workerAPIServer{}},
		{name: "unsupported operation", server: newServer(OperationStart, questionAnswerLifecyclePort{})},
		{name: "recover without saved lifecycle", server: newServer(OperationRecover, questionAnswerLifecyclePort{err: os.ErrNotExist})},
		{name: "lifecycle backend failure", server: newServer(OperationResume, questionAnswerLifecyclePort{err: errors.New("lifecycle unavailable")}), wantError: "load lifecycle"},
		{name: "wrong lifecycle identity", server: newServer(OperationResume, questionAnswerLifecyclePort{state: lifecycle.State{RunID: "another-run", TargetDir: target}}), wantError: "identity mismatch"},
		{name: "terminal lifecycle phase", server: newServer(OperationResume, questionAnswerLifecyclePort{state: lifecycle.State{RunID: runID, TargetDir: target, Phase: lifecycle.PhaseTerminal}})},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mount, err := testCase.server.prepareQuestionAnswerMount(context.Background())
			if testCase.wantError == "" {
				if mount != nil || err != nil {
					t.Fatalf("state should skip clarification admission: mount=%+v err=%v", mount, err)
				}
				return
			}
			if mount != nil || err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("invalid state did not fail closed: mount=%+v err=%v", mount, err)
			}
		})
	}
}

func TestWorkerAPIEventLogAuthorityAndRecorderIgnoreControllerOnlyCallbacks(t *testing.T) {
	var eventLog evidence.EventLog = &workerAPIEventLog{}
	marker, ok := eventLog.(interface{ ExternalEventAuthority() })
	if !ok {
		t.Fatal("worker event API must declare the controller as external event authority")
	}
	marker.ExternalEventAuthority()

	recorder := &workerAPIRecorder{}
	recorder.ReconcileInterrupted(time.Now())
	recorder.ApprovalDecided("run", "approval", "attempt", time.Now(), map[string]any{"action": "approve"})
	if recorder.firstErr != nil {
		t.Fatalf("controller-only callbacks must not emit worker API requests: %v", recorder.firstErr)
	}
}

type questionAnswerEventLog struct {
	events []evidence.Event
	err    error
}

func (l questionAnswerEventLog) Read(string) ([]evidence.Event, error) { return l.events, l.err }
func (l questionAnswerEventLog) ReadBytes(string) ([]byte, error)      { return nil, l.err }
func (questionAnswerEventLog) Append(string, evidence.Event, uint64, string) (evidence.Event, error) {
	return evidence.Event{}, nil
}

func TestValidateQuestionAnswerRequestEventRequiresMatchingControllerRequest(t *testing.T) {
	const runID, approvalID, attemptID = "question-event-run", "question-event-approval", "question-event-attempt"
	value := approval.PendingApproval{RunID: runID, ID: approvalID, AttemptID: attemptID, SubjectHash: "subject"}
	server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}

	readErr := errors.New("event authority unavailable")
	server.eventLogs = questionAnswerEventLog{err: readErr}
	if err := server.validateQuestionAnswerRequestEvent(value); !errors.Is(err, readErr) {
		t.Fatalf("event authority read failure should be returned: %v", err)
	}

	server.eventLogs = questionAnswerEventLog{events: []evidence.Event{
		{Type: "unrelated_event", AttemptID: attemptID},
		{Type: "approval_requested", AttemptID: "another-attempt", Data: map[string]any{
			"approval_id": approvalID, "subject_hash": value.SubjectHash, "status": string(approval.StatusPending),
		}},
		{Type: "approval_requested", AttemptID: attemptID, Data: map[string]any{
			"approval_id": 42, "subject_hash": value.SubjectHash, "status": string(approval.StatusPending),
		}},
	}}
	if err := server.validateQuestionAnswerRequestEvent(value); err == nil {
		t.Fatal("approval without an exact controller request event was accepted")
	}

	server.eventLogs = questionAnswerEventLog{events: []evidence.Event{{
		Type: "approval_requested", AttemptID: attemptID, Data: map[string]any{
			"approval_id": approvalID, "subject_hash": value.SubjectHash, "status": string(approval.StatusPending),
		},
	}}}
	if err := server.validateQuestionAnswerRequestEvent(value); err != nil {
		t.Fatalf("matching controller request event should authorize the durable approval: %v", err)
	}
}

func TestWorkerAPIDispatchRejectsCrossRunAndUnavailableAuthorityReads(t *testing.T) {
	const runID = "scoped-authority-run"
	server := &workerAPIServer{
		scope:        workerAPIScope{RunID: runID, Operation: OperationResume},
		usageAllowed: true, candidateEvidenceRoot: filepath.Join(t.TempDir(), "evidence"),
		eventLogs: questionAnswerEventLog{},
	}

	for _, method := range []string{"candidate.evidence.write", "candidate.evidence.read"} {
		if _, err := server.dispatch(method, workerAPICall{}); err == nil {
			t.Errorf("%s accepted a request without its scoped run identity", method)
		}
	}
	server.scope.Operation = OperationCancel
	for _, method := range []string{"candidate.evidence.write", "candidate.evidence.read"} {
		if _, err := server.dispatch(method, workerAPICall{RunID: runID}); err == nil {
			t.Errorf("%s was available to a cancel worker", method)
		}
	}
	server.scope.Operation = OperationResume
	if _, err := server.dispatch("event_log.read", workerAPICall{}); err == nil {
		t.Fatal("event log read accepted a request without its scoped run identity")
	}
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID, Offset: -1}); err == nil {
		t.Fatal("event log read accepted a negative offset")
	}
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID, Offset: 1}); err == nil {
		t.Fatal("first event log page accepted a nonzero offset")
	}
	server.scope.Operation = Operation("status")
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID}); err == nil {
		t.Fatal("event log read was available to an unsupported operation")
	}
	if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "run_started"}}); err == nil {
		t.Fatal("event log append was available to an unsupported operation")
	}
	server.scope.Operation = OperationResume

	readErr := errors.New("event bytes unavailable")
	server.eventLogs = questionAnswerEventLog{err: readErr}
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID}); !errors.Is(err, readErr) {
		t.Fatalf("event authority read failure should be returned: %v", err)
	}

	server.eventLogs = questionAnswerEventLog{}
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID, Snapshot: "stale-snapshot"}); err == nil {
		t.Fatal("event log read accepted an unavailable snapshot token")
	}
	server.eventSnapshot, server.eventSnapshotToken = []byte("event"), "snapshot"
	if _, err := server.dispatch("event_log.read", workerAPICall{RunID: runID, Snapshot: "snapshot", Offset: 6}); err == nil {
		t.Fatal("event log read accepted an offset beyond its snapshot")
	}

	if _, err := server.dispatch("event_log.append", workerAPICall{Event: evidence.Event{Type: "run_started"}}); err == nil {
		t.Fatal("event log append accepted a request without its scoped run identity")
	}
	if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID}); err == nil {
		t.Fatal("event log append accepted an empty event type")
	}
	server.eventLogs = questionAnswerEventLog{err: readErr}
	if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "run_started"}}); err == nil {
		t.Fatal("event log append ignored a failed authority read")
	}
}
