package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const (
	workerAPIAddressEnv               = "AI_TEAM_WORKER_API_ADDRESS"
	workerAPISocketEnv                = "AI_TEAM_WORKER_API_SOCKET"
	workerAPITokenEnv                 = "AI_TEAM_WORKER_API_TOKEN"
	workerAPIMaxBody                  = 1 << 20
	workerAPIMaxCandidateEvidenceBody = 9 << 20
	// AgentCLIRuntime bounds captured stdout/stderr diagnostics to 2 MiB each.
	// A 4 MiB event error can expand to 24 MiB when JSON escapes control bytes;
	// this 32 MiB envelope leaves room for the typed request and stays well
	// below the 64 MiB event-log limit.
	workerAPIMaxEventAppendEnvelope = 32 << 20
	workerAPIMaxEventAppendPayload  = 30 << 20
	// Attempt manifest bytes are base64 encoded once in the request Payload and
	// again in the read response. Leave room for both JSON envelopes while
	// keeping this allowance scoped to the typed manifest methods.
	workerAPIMaxAttemptManifestEnvelope = 12 << 20
	workerAPIMaxAttemptManifestPayload  = evidence.MaxAttemptManifestSize + workerAPIMaxBody
	// Approval reads may contain a large submitted markdown comment. Keep this
	// scoped to the typed approval read methods; the generic RPC cap stays 1 MiB.
	workerAPIMaxApprovalReadResponse = approval.MaxApprovalRecordBytes + (64 << 10)
	workerAPIApprovalListPageBytes   = 768 << 10
	workerAPIApprovalListPageItems   = 32
	workerAPIEventReadPage           = 512 << 10
	workerAPIErrorMarker             = "AI_TEAM_WORKER_API_RECORDER_ERROR:"
	workerAPIRequestTTL              = 30 * time.Second
	workerAPIFutureSkew              = 5 * time.Second
	workerAPIMaxNonces               = 4096
)

const WorkerAPIAddressEnv = workerAPIAddressEnv
const WorkerAPISocketEnv = workerAPISocketEnv
const WorkerAPITokenEnv = workerAPITokenEnv

type workerAPIScope struct {
	RunID       string    `json:"run_id"`
	Operation   Operation `json:"operation"`
	ExecutionID string    `json:"execution_id"`
	TargetDir   string    `json:"target_dir"`
}
type workerAPIRequest struct {
	workerAPIScope
	Method   string          `json:"method"`
	Payload  json.RawMessage `json:"payload"`
	Nonce    string          `json:"nonce"`
	IssuedAt time.Time       `json:"issued_at"`
}
type workerAPICall struct {
	RunID                  string                     `json:"run_id,omitempty"`
	A                      string                     `json:"a,omitempty"`
	B                      string                     `json:"b,omitempty"`
	C                      string                     `json:"c,omitempty"`
	Index                  int                        `json:"index,omitempty"`
	At                     time.Time                  `json:"at,omitempty"`
	Data                   map[string]any             `json:"data,omitempty"`
	IDs                    []string                   `json:"ids,omitempty"`
	Stage                  notifier.StageResult       `json:"stage,omitempty"`
	Error                  string                     `json:"error,omitempty"`
	Approval               approval.PendingApproval   `json:"approval,omitempty"`
	Lifecycle              lifecycle.State            `json:"lifecycle,omitempty"`
	Previous               lifecycle.State            `json:"previous_lifecycle,omitempty"`
	BriefVersion           pipeline.BriefVersion      `json:"brief_version,omitempty"`
	BriefContent           []byte                     `json:"brief_content,omitempty"`
	CandidateMetadata      candidate.Metadata         `json:"candidate_metadata,omitempty"`
	Usage                  metrics.UsageEnvelope      `json:"usage_envelope,omitempty"`
	TerminalRecord         delivery.TerminalRecord    `json:"terminal_record,omitempty"`
	Attestation            attest.Statement           `json:"attestation,omitempty"`
	ContainmentReceipt     *containment.Receipt       `json:"containment_receipt,omitempty"`
	CandidateEvidenceName  string                     `json:"candidate_evidence_name,omitempty"`
	CandidateEvidence      pipeline.CandidateEvidence `json:"candidate_evidence,omitempty"`
	AttemptManifest        evidence.AttemptManifest   `json:"attempt_manifest,omitempty"`
	Event                  evidence.Event             `json:"event,omitempty"`
	ExpectedSequence       uint64                     `json:"expected_sequence,omitempty"`
	ExpectedPreviousSHA256 string                     `json:"expected_previous_sha256,omitempty"`
	Offset                 int64                      `json:"offset,omitempty"`
	Snapshot               string                     `json:"snapshot,omitempty"`
}

type workerAPIEventPage struct {
	Snapshot string `json:"snapshot"`
	Offset   int64  `json:"offset"`
	Total    int64  `json:"total"`
	Data     []byte `json:"data"`
}

type workerAPIApprovalListPage struct {
	Values     []approval.PendingApproval `json:"values"`
	NextOffset int                        `json:"next_offset"`
	HasMore    bool                       `json:"has_more"`
}

type workerAPIEventAppendResult struct {
	SchemaVersion  int       `json:"schema_version"`
	Sequence       uint64    `json:"sequence"`
	RunID          string    `json:"run_id"`
	Type           string    `json:"type"`
	Stage          string    `json:"stage,omitempty"`
	AttemptID      string    `json:"attempt_id,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
	PreviousSHA256 string    `json:"previous_sha256"`
	SHA256         string    `json:"sha256"`
}
type workerQuestionPayload struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

type taskBoundBriefStore struct {
	pipeline.BriefStore
	expectedTask string
}

func (s *taskBoundBriefStore) Close() error {
	if s == nil || s.BriefStore == nil {
		return nil
	}
	if closer, ok := s.BriefStore.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (s *taskBoundBriefStore) CreateInitial(runID, intention string) (pipeline.BriefDocument, error) {
	if s == nil || s.BriefStore == nil || s.expectedTask == "" || intention != s.expectedTask {
		return pipeline.BriefDocument{}, errors.New("initial brief must match the controller's canonical task")
	}
	return s.BriefStore.CreateInitial(runID, s.expectedTask)
}

type workerApprovalPort interface {
	Create(approval.PendingApproval) (approval.PendingApproval, error)
	Load(string, string) (approval.PendingApproval, error)
	List(string) ([]approval.PendingApproval, error)
}
type workerAPIServer struct {
	scope                      workerAPIScope
	token                      string
	listener                   net.Listener
	socketPath                 string
	server                     *http.Server
	recorder                   pipeline.Recorder
	approvals                  workerApprovalPort
	lifecycle                  lifecycle.StorePort
	briefs                     pipeline.BriefStore
	candidates                 candidate.MetadataStore
	absences                   candidate.AbsenceMarkerStore
	usage                      metrics.FileUsageEnvelopeStore
	attestations               attest.ControllerStore
	containmentReceipts        containment.ControllerReceiptStore
	candidateEvidenceRoot      string
	attemptManifests           evidence.ControllerAttemptManifestStore
	questionAnswerStore        pipeline.ControllerQuestionAnswerStore
	questionAnswerID           string
	questionAnswerPath         string
	questionAnswerMount        *workerReadOnlyInputMount
	questionAnswerMountInfo    os.FileInfo
	questionAnswerMountCreated bool
	questionAnswerReplay       evidence.ReplayedRun
	eventLogs                  evidence.EventLog
	eventSnapshot              []byte
	eventSnapshotToken         string
	usageAllowed               bool
	usageEnvelopeWritten       bool
	candidateAbsenceAllowed    bool
	briefTask                  string
	approvedPlanHash           string
	dispatchMu                 sync.Mutex
	requestSlots               chan struct{}
	nonceMu                    sync.Mutex
	nonces                     map[string]time.Time
}

// startWorkerAPIServer creates a per-execution loopback capability for
// unsandboxed launches. Sandboxed Linux launches use startWorkerAPIServerUnix.
// The worker
// can report pipeline events and request/read approvals only for this run.
func startWorkerAPIServer(job Job, recorder pipeline.Recorder, approvals workerApprovalPort) (*workerAPIServer, error) {
	return startWorkerAPIServerForTask(job, recorder, approvals, job.Task)
}

func startWorkerAPIServerForTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, expectedTask string) (*workerAPIServer, error) {
	return startWorkerAPIServerWithTaskAndListener(job, recorder, approvals, func() (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}, rand.Reader, expectedTask)
}

func startWorkerAPIServerWithListener(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listen func() (net.Listener, error), random io.Reader) (*workerAPIServer, error) {
	return startWorkerAPIServerWithTaskAndListener(job, recorder, approvals, listen, random, job.Task)
}

func startWorkerAPIServerWithTaskAndListener(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listen func() (net.Listener, error), random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if recorder == nil || approvals == nil {
		return nil, errors.New("worker controller API requires recorder and approval ports")
	}
	listener, err := listen()
	if err != nil {
		return nil, err
	}
	return serveWorkerAPI(job, recorder, approvals, listener, "", random, expectedTask)
}

func startWorkerAPIServerUnix(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixForTask(job, recorder, approvals, socketPath, job.Task)
}

func startWorkerAPIServerUnixForTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath, expectedTask string) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixWithTask(job, recorder, approvals, socketPath, net.Listen, os.Chmod, rand.Reader, expectedTask)
}

func startWorkerAPIServerUnixWith(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string, listen func(string, string) (net.Listener, error), chmod func(string, os.FileMode) error, random io.Reader) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixWithTask(job, recorder, approvals, socketPath, listen, chmod, random, job.Task)
}

func startWorkerAPIServerUnixWithTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string, listen func(string, string) (net.Listener, error), chmod func(string, os.FileMode) error, random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if recorder == nil || approvals == nil {
		return nil, errors.New("worker controller API requires recorder and approval ports")
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return nil, errors.New("worker controller API socket path must be absolute and clean")
	}
	listener, err := listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on worker controller API socket: %w", err)
	}
	if err := chmod(socketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("secure worker controller API socket: %w", err)
	}
	return serveWorkerAPI(job, recorder, approvals, listener, socketPath, random, expectedTask)
}

func serveWorkerAPI(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listener net.Listener, socketPath string, random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if err := evidence.ValidateRunID(job.RunID); err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, fmt.Errorf("worker controller API run id: %w", err)
	}
	var nonce [32]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, err
	}
	fileCandidateStore := candidate.FileMetadataStore{}
	canonicalTarget, err := candidate.CanonicalTargetDir(job.TargetDir)
	if err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, fmt.Errorf("canonicalize worker API attestation target: %w", err)
	}
	briefStore := pipeline.NewControllerBriefStore(canonicalTarget)
	if err := briefStore.PrepareRun(job.RunID); err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, fmt.Errorf("prepare controller business brief store: %w", err)
	}
	api := &workerAPIServer{scope: workerAPIScope{RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, TargetDir: job.TargetDir}, token: hex.EncodeToString(nonce[:]), listener: listener, socketPath: socketPath, recorder: recorder, approvals: approvals, briefs: &taskBoundBriefStore{BriefStore: briefStore, expectedTask: expectedTask}, candidates: fileCandidateStore, absences: fileCandidateStore, briefTask: expectedTask, requestSlots: make(chan struct{}, 1), nonces: make(map[string]time.Time)}
	api.approvedPlanHash = strings.ToLower(strings.TrimSpace(job.ApprovePlanHash))
	api.usageAllowed = socketPath != ""
	if api.usageAllowed {
		store := evidence.ControllerEventStore{TargetDir: canonicalTarget}
		runDir := filepath.Join(canonicalTarget, ".ai-team", "runs", job.RunID)
		reserved, sourceErr := store.IsReserved(job.RunID)
		if sourceErr != nil {
			// Pre-event-store cloud runs are recognizable from independent
			// controller markers, but ordinary readers must not fall back to the
			// worker-visible journal. Resume/Recover/Cancel are the only explicit
			// migration admissions, and migration itself rejects any event-era
			// authority proof.
			legacyCandidate, candidateErr := store.IsLegacyMigrationCandidate(job.RunID)
			if candidateErr != nil {
				sourceErr = errors.Join(sourceErr, candidateErr)
			} else if legacyCandidate {
				switch job.Operation {
				case OperationResume, OperationRecover, OperationCancel:
					if _, statErr := os.Lstat(runDir); statErr != nil {
						sourceErr = fmt.Errorf("inspect legacy event run directory: %w", statErr)
					} else {
						sourceErr = store.MigrateLegacyWithValidator(job.RunID, runDir, func(events []evidence.Event) error {
							return validateControllerDeliveryClaims(job, events, approvals)
						})
						if sourceErr == nil {
							reserved = true
						}
					}
				default:
					// Do not migrate an old worker-visible journal for read-only API scopes.
				}
			}
			if sourceErr != nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				return nil, fmt.Errorf("inspect controller event reservation: %w", sourceErr)
			}
		}
		if !reserved {
			switch job.Operation {
			case OperationStart:
				sourceErr = store.Reserve(job.RunID)
			case OperationRecover:
				if _, statErr := os.Lstat(runDir); errors.Is(statErr, os.ErrNotExist) {
					// A recover dispatch with no prior worker evidence may proceed as
					// a fresh admitted run; its authority must exist before spawn too.
					sourceErr = store.Reserve(job.RunID)
				} else if statErr != nil {
					sourceErr = fmt.Errorf("inspect recovery run directory: %w", statErr)
				} else {
					sourceErr = store.MigrateLegacyWithValidator(job.RunID, runDir, func(events []evidence.Event) error {
						return validateControllerDeliveryClaims(job, events, approvals)
					})
				}
			case OperationResume, OperationCancel:
				sourceErr = store.MigrateLegacyWithValidator(job.RunID, runDir, func(events []evidence.Event) error {
					return validateControllerDeliveryClaims(job, events, approvals)
				})
			default:
				// Event appends are unavailable for operations that do not own a
				// run lifecycle. Keep their controller API surface read-only.
			}
			if sourceErr != nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				return nil, fmt.Errorf("prepare controller event log: %w", sourceErr)
			}
			reserved, sourceErr = store.IsReserved(job.RunID)
			if sourceErr != nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				return nil, fmt.Errorf("verify controller event reservation: %w", sourceErr)
			}
		}
		if reserved {
			if job.Operation == OperationResume || job.Operation == OperationRecover {
				events, readErr := store.Read(job.RunID)
				if readErr == nil {
					readErr = validateControllerDeliveryClaims(job, events, approvals)
				}
				if readErr != nil {
					_ = listener.Close()
					_ = os.Remove(socketPath)
					return nil, fmt.Errorf("validate controller delivery authority: %w", readErr)
				}
			}
			pinned, pinErr := store.OpenReserved(job.RunID)
			if pinErr != nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				return nil, fmt.Errorf("pin controller event log: %w", pinErr)
			}
			api.eventLogs = pinned
		}
	}
	if api.usageAllowed {
		// Establish per-run event authority before independent cloud markers.
		// Otherwise IsReserved would mistake a brand-new Start for a legacy
		// cloud run while the event roots are still empty.
		if err := (metrics.FileUsageEnvelopeStore{}).Reserve(canonicalTarget, job.RunID); err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			return nil, fmt.Errorf("prepare controller usage store: %w", err)
		}
		if job.Operation == OperationStart {
			if err := (evidence.ControllerAttemptManifestStore{TargetDir: canonicalTarget}).Reserve(job.RunID); err != nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				return nil, fmt.Errorf("reserve controller attempt manifest store: %w", err)
			}
		}
	}
	if api.usageAllowed {
		api.candidateEvidenceRoot = filepath.Join(canonicalTarget, ".ai-team", "state", "evidence")
	}
	api.attestations = attest.ControllerStore{TargetDir: canonicalTarget}
	api.attemptManifests = evidence.ControllerAttemptManifestStore{TargetDir: canonicalTarget}
	api.containmentReceipts = containment.ControllerReceiptStore{TargetDir: canonicalTarget}
	api.questionAnswerStore = pipeline.ControllerQuestionAnswerStore{TargetDir: canonicalTarget}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/call", api.handle)
	api.server = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = api.server.Serve(listener) }()
	return api, nil
}
func (s *workerAPIServer) close() {
	if s != nil && s.server != nil {
		_ = s.server.Close()
		s.cleanupQuestionAnswerMountpoint()
		if s.socketPath != "" {
			_ = os.Remove(s.socketPath)
		}
		if closer, ok := s.briefs.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if closer, ok := s.eventLogs.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

func (s *workerAPIServer) prepareQuestionAnswerMount(ctx context.Context) (*workerReadOnlyInputMount, error) {
	if s == nil || !s.usageAllowed || s.eventLogs == nil || s.lifecycle == nil || s.approvals == nil {
		return nil, nil
	}
	switch s.scope.Operation {
	case OperationResume, OperationRecover:
	default:
		return nil, nil
	}
	state, err := s.lifecycle.Load(s.scope.RunID)
	if errors.Is(err, os.ErrNotExist) && s.scope.Operation == OperationRecover {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load lifecycle before clarification materialization: %w", err)
	}
	if state.RunID != s.scope.RunID || state.TargetDir != s.scope.TargetDir {
		return nil, errors.New("clarification materialization lifecycle identity mismatch")
	}
	if state.Phase != lifecycle.PhaseWaiting && state.Phase != lifecycle.PhaseRunning && state.Phase != lifecycle.PhaseResumable {
		return nil, nil
	}
	runRoot := filepath.Join(s.questionAnswerStore.TargetDir, ".ai-team", "runs")
	manifestSource := evidence.ReservedAttemptManifestSource{TargetDir: s.questionAnswerStore.TargetDir}
	_, _, replayed, err := evidence.ResumeWithEventLog(runRoot, s.scope.RunID, s.eventLogs, manifestSource)
	if err != nil {
		return nil, fmt.Errorf("verify run before clarification materialization: %w", err)
	}

	var value *approval.PendingApproval
	switch state.Phase {
	case lifecycle.PhaseWaiting:
		if state.NextStage == "" || state.PendingApprovalID == "" {
			return nil, nil
		}
		loaded, loadErr := s.approvals.Load(s.scope.RunID, state.PendingApprovalID)
		if loadErr != nil {
			return nil, fmt.Errorf("load pending clarification approval: %w", loadErr)
		}
		if loaded.Status != approval.StatusResolved || loaded.ResolvedAction != "answer_questions" {
			return nil, nil
		}
		if loaded.FromStage != state.NextStage || loaded.ToStage != state.NextStage || loaded.Targets[loaded.ResolvedAction] != state.NextStage {
			return nil, errors.New("clarification approval stage does not match waiting lifecycle stage")
		}
		value = &loaded
	case lifecycle.PhaseRunning, lifecycle.PhaseResumable:
		value, err = pipeline.RecoveredQuestionApproval(s.approvals, s.scope.RunID, state.NextStage, replayed)
		if err != nil {
			// A stale clarification can coexist with a later, verified graph
			// transition when the process crashed after transition_selected but
			// before updating the lifecycle checkpoint. Let Pipeline reconcile
			// that transition. It will still fail closed if no valid handoff is
			// present. Only suppress this sentinel at the pre-spawn admission
			// boundary; the worker dispatch path repeats approval validation.
			if errors.Is(err, pipeline.ErrStaleQuestionApproval) {
				return nil, nil
			}
			return nil, fmt.Errorf("select recovered clarification approval: %w", err)
		}
	}
	if value == nil {
		return nil, nil
	}
	if err := pipeline.ValidateQuestionAnswerApproval(*value, replayed); err != nil {
		return nil, fmt.Errorf("validate clarification source attempt: %w", err)
	}
	if err := validateQuestionAnswerCandidate(ctx, s.questionAnswerStore.TargetDir, *value); err != nil {
		return nil, err
	}
	if err := s.validateQuestionAnswerRequestEvent(*value); err != nil {
		return nil, err
	}
	_, manifest, err := evidence.ReadAttemptManifest(manifestSource, filepath.Join(runRoot, s.scope.RunID), s.scope.RunID, value.AttemptID)
	if err != nil {
		return nil, fmt.Errorf("read clarification source attempt manifest: %w", err)
	}
	if manifest.RunID != s.scope.RunID || manifest.AttemptID != value.AttemptID || manifest.Stage != value.FromStage {
		return nil, errors.New("clarification source manifest identity mismatch")
	}
	hasQuestions := false
	for _, output := range manifest.Outputs {
		if output.Name == "questions" {
			hasQuestions = true
			break
		}
	}
	if !hasQuestions {
		return nil, errors.New("clarification source manifest has no questions output")
	}
	source, err := s.questionAnswerStore.Prepare(*value)
	if err != nil {
		return nil, fmt.Errorf("prepare canonical clarification answer: %w", err)
	}
	expectedAnswer, err := pipeline.CanonicalQuestionAnswerContent(*value)
	if err != nil {
		return nil, fmt.Errorf("derive canonical clarification answer: %w", err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(s.questionAnswerStore.TargetDir, s.scope.RunID, value.ID)
	if err != nil {
		return nil, err
	}
	created, mountInfo, err := prepareQuestionAnswerMountpoint(destination, expectedAnswer)
	if err != nil {
		return nil, err
	}
	s.questionAnswerID = value.ID
	s.questionAnswerPath = destination
	s.questionAnswerReplay = replayed
	s.questionAnswerMountInfo = mountInfo
	s.questionAnswerMountCreated = created
	s.questionAnswerMount = &workerReadOnlyInputMount{SourcePath: source, TargetPath: destination, TargetInfo: mountInfo}
	return s.questionAnswerMount, nil
}

func prepareQuestionAnswerMountpoint(path string, expected []byte) (bool, os.FileInfo, error) {
	if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
		return false, nil, err
	}
	if data, info, err := readQuestionAnswerMountFile(path, maxQuestionAnswerMountBytes); err == nil {
		if len(data) == 0 {
			return false, info, nil
		}
		if len(expected) == 0 || !bytes.Equal(data, expected) || info.Mode().Perm()&0o222 != 0 {
			return false, nil, errors.New("existing clarification input is not an exact read-only durable-answer projection")
		}
		// A previous invocation may have crashed after writing this exact
		// projection. Keep it as the target of the production read-only bind
		// mount; after unmount it still contains only the canonical answer.
		return false, info, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, nil, err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return false, nil, err
	}
	_, info, err := readQuestionAnswerMountFile(path, maxQuestionAnswerMountBytes)
	if err != nil {
		return false, nil, err
	}
	return true, info, nil
}

func (s *workerAPIServer) cleanupQuestionAnswerMountpoint() {
	if s == nil || s.questionAnswerPath == "" || s.questionAnswerMountInfo == nil || !s.questionAnswerMountCreated {
		return
	}
	current, err := os.Lstat(s.questionAnswerPath)
	if err == nil && current.Mode().IsRegular() && current.Mode()&os.ModeSymlink == 0 &&
		current.Size() == 0 && os.SameFile(current, s.questionAnswerMountInfo) {
		if data, verified, readErr := readQuestionAnswerMountFile(s.questionAnswerPath, maxQuestionAnswerMountBytes); readErr == nil &&
			len(data) == 0 && os.SameFile(current, verified) {
			_ = os.Remove(s.questionAnswerPath)
		}
	}
}

func (s *workerAPIServer) validateQuestionAnswerRequestEvent(value approval.PendingApproval) error {
	events, err := s.eventLogs.Read(s.scope.RunID)
	if err != nil {
		return fmt.Errorf("read clarification approval event authority: %w", err)
	}
	for _, event := range events {
		if event.Type != "approval_requested" || event.AttemptID != value.AttemptID {
			continue
		}
		id, _ := event.Data["approval_id"].(string)
		subject, _ := event.Data["subject_hash"].(string)
		status, _ := event.Data["status"].(string)
		if id == value.ID && subject == value.SubjectHash && status == string(approval.StatusPending) {
			return nil
		}
	}
	return errors.New("clarification approval has no matching controller event request")
}

func (s *workerAPIServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/call" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.requestSlots != nil {
		select {
		case s.requestSlots <- struct{}{}:
			defer func() { <-s.requestSlots }()
		case <-r.Context().Done():
			return
		}
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, workerAPIMaxEventAppendEnvelope+1))
	if err != nil || len(data) > workerAPIMaxEventAppendEnvelope {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	requestLimit := workerAPIRequestBodyLimit("", len(data))
	requestMethod := ""
	if len(data) > workerAPIMaxBody {
		var largeRequest workerAPIRequest
		if json.Unmarshal(data, &largeRequest) != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		requestMethod = largeRequest.Method
		requestLimit = workerAPIRequestBodyLimit(largeRequest.Method, len(data))
	}
	if requestLimit == 0 || len(data) > requestLimit || !workerAPIRequestWithinLimit(requestMethod, len(data)) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request workerAPIRequest
	if strictjson.Unmarshal(data, int64(requestLimit), &request) != nil || request.workerAPIScope != s.scope {
		http.Error(w, "invalid invocation scope", http.StatusForbidden)
		return
	}
	var call workerAPICall
	payloadLimit := workerAPIMaxBody
	if request.Method == "candidate.evidence.write" || request.Method == "candidate.evidence.read" || request.Method == "attempt_manifest.write" {
		payloadLimit = workerAPIMaxCandidateEvidenceBody
	}
	if request.Method == "attempt_manifest.write" {
		payloadLimit = workerAPIMaxAttemptManifestPayload
	}
	if request.Method == "event_log.append" || request.Method == "event_log.description_missing" {
		payloadLimit = workerAPIMaxEventAppendPayload
	}
	if len(request.Payload) > 0 && strictjson.Unmarshal(request.Payload, int64(payloadLimit), &call) != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !s.acceptRequest(time.Now(), request.Nonce, request.IssuedAt) {
		http.Error(w, "invalid, expired, replayed, or exhausted request nonce", http.StatusUnauthorized)
		return
	}
	result, err := s.dispatch(request.Method, call)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		result = struct{}{}
	}
	_ = json.NewEncoder(w).Encode(result)
}

func workerAPIRequestBodyLimit(method string, size int) int {
	switch method {
	case "candidate.evidence.write", "candidate.evidence.read":
		return workerAPIMaxCandidateEvidenceBody
	case "attempt_manifest.write", "attempt_manifest.read":
		return workerAPIMaxAttemptManifestEnvelope
	case "event_log.append", "event_log.description_missing":
		return workerAPIMaxEventAppendEnvelope
	default:
		if size <= workerAPIMaxBody {
			return workerAPIMaxBody
		}
		return 0
	}
}

func workerAPIRequestWithinLimit(method string, size int) bool {
	limit := workerAPIRequestBodyLimit(method, size)
	return limit > 0 && size >= 0 && size <= limit
}

type workerAPIResponseError struct {
	status  int
	message string
}

func (e workerAPIResponseError) Error() string { return e.message }
func (e workerAPIResponseError) Is(target error) bool {
	return e.status == http.StatusNotFound && target == os.ErrNotExist
}

// acceptRequest binds one authenticated API operation to a fresh, short-lived
// nonce. The map is capped per invocation; when it is full, requests fail
// closed until expired entries can be reclaimed.
func (s *workerAPIServer) acceptRequest(now time.Time, nonce string, issuedAt time.Time) bool {
	if len(nonce) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 32 || issuedAt.IsZero() || !issuedAt.After(now.Add(-workerAPIRequestTTL)) || issuedAt.After(now.Add(workerAPIFutureSkew)) {
		return false
	}
	// Hex decoding accepts either case. Key by the decoded bytes' canonical
	// representation so changing hex letter case cannot bypass replay checks.
	nonceKey := hex.EncodeToString(decoded)
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	for existing, expiresAt := range s.nonces {
		if !now.Before(expiresAt) {
			delete(s.nonces, existing)
		}
	}
	if _, exists := s.nonces[nonceKey]; exists || len(s.nonces) >= workerAPIMaxNonces {
		return false
	}
	s.nonces[nonceKey] = issuedAt.Add(workerAPIRequestTTL)
	return true
}

func (s *workerAPIServer) appendVerifiedClarification(approvalID, questions, submittedAnswer string) (any, error) {
	if approvalID == "" {
		return nil, errors.New("clarification approval id is required")
	}
	value, err := s.approvals.Load(s.scope.RunID, approvalID)
	if err != nil {
		return nil, fmt.Errorf("load clarification approval: %w", err)
	}
	if value.RunID != s.scope.RunID || value.ID != approvalID {
		return nil, errors.New("clarification approval identity mismatch")
	}
	if value.Status != approval.StatusResolved || value.ResolvedAction != "answer_questions" || value.ResolvedAt.IsZero() {
		return nil, errors.New("clarification requires a resolved answer_questions approval")
	}
	if value.Kind != approval.KindQuestions || value.Trigger != "graph_outcome:blocked" ||
		value.Targets["answer_questions"] != value.FromStage || !containsWorkerString(value.Actions, "answer_questions") {
		return nil, errors.New("approval is not a stage questions request")
	}
	var payload workerQuestionPayload
	if len(value.Payload) == 0 || json.Unmarshal(value.Payload, &payload) != nil ||
		payload.Kind != "questions" || strings.TrimSpace(payload.Markdown) == "" {
		return nil, errors.New("clarification approval has no durable questions")
	}
	if questions != payload.Markdown {
		return nil, errors.New("clarification questions do not match the durable approval")
	}
	var resolvingDecision *approval.Decision
	for index := range value.Decisions {
		decision := &value.Decisions[index]
		if decision.Action != "answer_questions" || !containsWorkerString(value.RequiredRoles, decision.ActorRole) {
			return nil, errors.New("clarification decision is invalid")
		}
		if decision.ApprovalID != value.ID || decision.ActorID == "" ||
			decision.SubjectHash != value.SubjectHash || decision.DecidedAt.IsZero() || strings.TrimSpace(decision.Comment) == "" {
			return nil, errors.New("clarification decision is invalid")
		}
		if decision.DecidedAt.Equal(value.ResolvedAt) {
			if resolvingDecision != nil {
				return nil, errors.New("clarification approval has ambiguous resolving decisions")
			}
			resolvingDecision = decision
		}
	}
	if resolvingDecision == nil {
		return nil, errors.New("clarification approval has no resolving durable decision")
	}
	answer := strings.TrimSpace(resolvingDecision.Comment)
	if strings.TrimSpace(submittedAnswer) != answer {
		return nil, errors.New("clarification answer does not match the durable decision")
	}
	provenance, err := pipeline.QuestionAnswerProvenance(value)
	if err != nil {
		return nil, fmt.Errorf("clarification durable provenance: %w", err)
	}
	return s.briefs.AppendClarification(s.scope.RunID, value.ID, provenance, payload.Markdown, answer)
}

func containsWorkerString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *workerAPIServer) dispatch(method string, c workerAPICall) (any, error) {
	// Recorder implementations such as web.StoreRecorder keep per-run sequence
	// and stage state. Requests from one child can arrive concurrently, so keep
	// each invocation's projection ordered and race-free.
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if err := evidence.ValidateRunID(s.scope.RunID); err != nil {
		return nil, fmt.Errorf("worker API scope run id: %w", err)
	}
	if c.RunID != "" && c.RunID != s.scope.RunID {
		return nil, errors.New("worker API run mismatch")
	}
	switch method {
	case "brief.create_initial":
		if s.briefTask == "" || c.A != s.briefTask {
			return nil, errors.New("initial brief must match the controller's canonical task")
		}
		return s.briefs.CreateInitial(s.scope.RunID, c.A)
	case "brief.append_clarification":
		return s.appendVerifiedClarification(c.A, c.B, c.C)
	case "brief.list":
		return s.briefs.List(s.scope.RunID)
	case "brief.read":
		return s.briefs.Read(s.scope.RunID, c.A)
	case "candidate.metadata.create":
		switch s.scope.Operation {
		case OperationStart, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API candidate metadata create is not allowed for operation %q", s.scope.Operation)
		}
		m := c.CandidateMetadata
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		if err := candidate.ValidateMetadata(target, s.scope.RunID, m); err != nil {
			return nil, err
		}
		return nil, s.candidates.Create(m)
	case "candidate.metadata.read":
		if c.A != s.scope.RunID {
			return nil, errors.New("candidate metadata run mismatch")
		}
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		m, err := s.candidates.Read(target, s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if err := candidate.ValidateMetadata(target, s.scope.RunID, m); err != nil {
			return nil, err
		}
		return m, nil
	case "candidate.absence.read":
		switch s.scope.Operation {
		case OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API candidate absence read is not allowed for operation %q", s.scope.Operation)
		}
		if c.A != s.scope.RunID {
			return nil, errors.New("candidate absence run mismatch")
		}
		if !s.candidateAbsenceAllowed || s.absences == nil {
			return nil, errors.New("controller has no candidate absence admission for this run")
		}
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		if err := s.absences.ReadAbsent(target, s.scope.RunID); err != nil {
			return nil, err
		}
		return nil, nil
	case "candidate.absence.create":
		return nil, errors.New("worker API cannot create candidate absence markers")
	case "candidate.evidence.write":
		if !s.usageAllowed || s.candidateEvidenceRoot == "" {
			return nil, errors.New("controller candidate evidence writes require bubblewrap Unix transport")
		}
		if c.RunID != s.scope.RunID {
			return nil, errors.New("candidate evidence run mismatch")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API candidate evidence write is not allowed for operation %q", s.scope.Operation)
		}
		if err := pipeline.ValidateCandidateEvidence(s.scope.RunID, c.CandidateEvidenceName, c.CandidateEvidence); err != nil {
			return nil, err
		}
		return nil, writeControllerCandidateEvidence(s.candidateEvidenceRoot, s.scope.RunID, c.CandidateEvidenceName, c.CandidateEvidence)
	case "candidate.evidence.read":
		if !s.usageAllowed || s.candidateEvidenceRoot == "" {
			return nil, errors.New("controller candidate evidence reads require bubblewrap Unix transport")
		}
		if c.RunID != s.scope.RunID {
			return nil, errors.New("candidate evidence run mismatch")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API candidate evidence read is not allowed for operation %q", s.scope.Operation)
		}
		return readControllerCandidateEvidence(s.candidateEvidenceRoot, s.scope.RunID, c.CandidateEvidenceName)
	case "event_log.read":
		if !s.usageAllowed || s.eventLogs == nil {
			return nil, errors.New("controller event log reads require bubblewrap Unix transport")
		}
		if c.RunID != s.scope.RunID || c.Offset < 0 {
			return nil, errors.New("event log read run or offset mismatch")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover, OperationCancel:
		default:
			return nil, fmt.Errorf("worker API event log read is not allowed for operation %q", s.scope.Operation)
		}
		if c.Snapshot == "" {
			if c.Offset != 0 {
				return nil, errors.New("event log read must start at offset zero")
			}
			data, err := s.eventLogs.ReadBytes(s.scope.RunID)
			if err != nil {
				return nil, err
			}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return nil, err
			}
			s.eventSnapshot = data
			s.eventSnapshotToken = hex.EncodeToString(token[:])
			c.Snapshot = s.eventSnapshotToken
		} else if c.Snapshot != s.eventSnapshotToken || s.eventSnapshot == nil {
			return nil, errors.New("event log read snapshot is unavailable")
		}
		if c.Offset > int64(len(s.eventSnapshot)) {
			return nil, errors.New("event log read offset exceeds snapshot")
		}
		end := c.Offset + workerAPIEventReadPage
		if end > int64(len(s.eventSnapshot)) {
			end = int64(len(s.eventSnapshot))
		}
		page := workerAPIEventPage{
			Snapshot: s.eventSnapshotToken, Offset: c.Offset, Total: int64(len(s.eventSnapshot)),
			Data: append([]byte(nil), s.eventSnapshot[c.Offset:end]...),
		}
		if end == int64(len(s.eventSnapshot)) {
			s.eventSnapshot = nil
			s.eventSnapshotToken = ""
		}
		return page, nil
	case "event_log.append", "event_log.description_missing":
		if !s.usageAllowed || s.eventLogs == nil {
			return nil, errors.New("controller event log appends require bubblewrap Unix transport")
		}
		if c.RunID != s.scope.RunID {
			return nil, errors.New("event log append run mismatch")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover, OperationCancel:
		default:
			return nil, fmt.Errorf("worker API event log append is not allowed for operation %q", s.scope.Operation)
		}
		if c.Event.Type == "" {
			return nil, errors.New("event log append requires event type")
		}
		events, err := s.eventLogs.Read(s.scope.RunID)
		if err != nil {
			return nil, fmt.Errorf("read event chain before append: %w", err)
		}
		runDir := filepath.Join(s.scope.TargetDir, ".ai-team", "runs", s.scope.RunID)
		controllerOnly := method == "event_log.description_missing"
		validateAppend := evidence.ValidateEventAppend
		if controllerOnly {
			validateAppend = evidence.ValidateControllerEventAppend
			if s.scope.Operation != OperationStart && s.scope.Operation != OperationResume && s.scope.Operation != OperationRecover {
				return nil, fmt.Errorf("controller description event is not allowed for operation %q", s.scope.Operation)
			}
		}
		validated, exactRetry, err := validateAppend(events, s.scope.RunID, runDir, c.Event,
			c.ExpectedSequence, c.ExpectedPreviousSHA256, evidence.ReservedAttemptManifestSource{TargetDir: s.scope.TargetDir})
		if err != nil {
			return nil, err
		}
		if controllerOnly {
			if err := s.validateControllerDescriptionMissing(c.Event, events); err != nil {
				return nil, err
			}
		}
		if !controllerOnly && !exactRetry {
			if err := s.validateWorkerApprovalEvent(c.Event); err != nil {
				return nil, err
			}
			if err := s.validateWorkerDeliveryEvent(c.Event, events, runDir); err != nil {
				return nil, err
			}
			if err := s.validateWorkerTerminalEvent(c.Event); err != nil {
				return nil, err
			}
		}
		if !exactRetry {
			c.Event = validated
		}
		appended, err := s.eventLogs.Append(s.scope.RunID, c.Event, c.ExpectedSequence, c.ExpectedPreviousSHA256)
		if err != nil {
			return nil, err
		}
		s.eventSnapshot = nil
		s.eventSnapshotToken = ""
		return workerAPIEventAppendResult{
			SchemaVersion: appended.SchemaVersion, Sequence: appended.Sequence, RunID: appended.RunID,
			Type: appended.Type, Stage: appended.Stage, AttemptID: appended.AttemptID, Timestamp: appended.Timestamp,
			PreviousSHA256: appended.PreviousSHA256, SHA256: appended.SHA256,
		}, nil
	case "attempt_manifest.write":
		if !s.usageAllowed {
			return nil, errors.New("controller attempt manifest writes require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API attempt manifest write is not allowed for operation %q", s.scope.Operation)
		}
		reserved, err := s.attemptManifests.IsReserved(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if !reserved {
			// Older runs have no reservation and keep their filesystem layout.
			// New bubblewrap starts reserve before spawn, so this path can only be
			// used for legacy recovery/resume.
			return nil, nil
		}
		if c.AttemptManifest.HumanSubmissionVersion > 0 {
			if err := s.validateHumanSubmissionManifest(c.AttemptManifest); err != nil {
				return nil, err
			}
		}
		return nil, s.attemptManifests.Write(s.scope.RunID, c.AttemptManifest)
	case "attempt_manifest.read":
		if !s.usageAllowed {
			return nil, errors.New("controller attempt manifest reads require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API attempt manifest read is not allowed for operation %q", s.scope.Operation)
		}
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		runDir := filepath.Join(target, ".ai-team", "runs", s.scope.RunID)
		return (evidence.ReservedAttemptManifestSource{TargetDir: target}).ReadAttemptManifest(runDir, s.scope.RunID, c.A)
	case "usage.envelope.write":
		if !s.usageAllowed {
			return nil, errors.New("controller usage writes require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API usage write is not allowed for operation %q", s.scope.Operation)
		}
		if err := metrics.ValidateUsageEnvelope(s.scope.RunID, c.Usage); err != nil {
			return nil, err
		}
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		if s.usageEnvelopeWritten {
			stored, readErr := metrics.ReadUsageEnvelope(target, s.scope.RunID)
			if readErr == nil && reflect.DeepEqual(stored, c.Usage) {
				return nil, nil
			}
			return nil, errors.New("usage envelope already submitted by this invocation")
		}
		var writeErr error
		if s.scope.Operation == OperationResume || s.scope.Operation == OperationRecover {
			writeErr = s.usage.Replace(target, s.scope.RunID, c.Usage)
		} else {
			writeErr = s.usage.Write(target, s.scope.RunID, c.Usage)
		}
		if writeErr != nil {
			return nil, writeErr
		}
		s.usageEnvelopeWritten = true
		return nil, nil
	case "delivery.terminal_record.write":
		if !s.usageAllowed {
			return nil, errors.New("controller delivery writes require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API terminal delivery write is not allowed for operation %q", s.scope.Operation)
		}
		record := c.TerminalRecord
		if record.RunID != s.scope.RunID {
			return nil, errors.New("terminal delivery record run mismatch")
		}
		if err := record.Validate(); err != nil {
			return nil, err
		}
		target, err := candidate.CanonicalTargetDir(s.scope.TargetDir)
		if err != nil {
			return nil, err
		}
		return nil, delivery.WriteControllerTerminalRecord(target, s.scope.RunID, record)
	case "attestation.write":
		if !s.usageAllowed || s.attestations.TargetDir == "" {
			return nil, errors.New("controller attestation writes require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API attestation write is not allowed for operation %q", s.scope.Operation)
		}
		if c.Attestation.Predicate.RunID != s.scope.RunID {
			return nil, errors.New("attestation run mismatch")
		}
		raw, err := json.Marshal(c.Attestation)
		if err != nil {
			return nil, err
		}
		return nil, s.attestations.Write(s.scope.RunID, raw)
	case "containment.write":
		if !s.usageAllowed || s.containmentReceipts.TargetDir == "" {
			return nil, errors.New("controller containment writes require bubblewrap Unix transport")
		}
		switch s.scope.Operation {
		case OperationStart, OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API containment write is not allowed for operation %q", s.scope.Operation)
		}
		if c.ContainmentReceipt == nil {
			return nil, errors.New("containment receipt is required")
		}
		return nil, s.containmentReceipts.Write(s.scope.RunID, *c.ContainmentReceipt)
	case "approval.create":
		if c.Approval.RunID != "" && c.Approval.RunID != s.scope.RunID {
			return nil, errors.New("approval run mismatch")
		}
		c.Approval.RunID = s.scope.RunID
		return s.approvals.Create(c.Approval)
	case "approval.load":
		value, err := s.approvals.Load(s.scope.RunID, c.A)
		if err != nil {
			return nil, err
		}
		if encoded, err := json.Marshal(value); err != nil || len(encoded) > approval.MaxApprovalRecordBytes {
			return nil, errors.New("approval record exceeds maximum size")
		}
		return value, nil
	case "approval.list":
		values, err := s.approvals.List(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if c.Index < 0 || c.Index > len(values) {
			return nil, errors.New("approval list offset is invalid")
		}
		sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
		page := workerAPIApprovalListPage{Values: make([]approval.PendingApproval, 0, workerAPIApprovalListPageItems)}
		pageBytes := 2 // JSON array delimiters
		for index := c.Index; index < len(values) && len(page.Values) < workerAPIApprovalListPageItems; index++ {
			encoded, err := json.Marshal(values[index])
			if err != nil || len(encoded) > approval.MaxApprovalRecordBytes {
				return nil, errors.New("approval list record exceeds maximum size")
			}
			if len(page.Values) > 0 && pageBytes+len(encoded)+1 > workerAPIApprovalListPageBytes {
				break
			}
			page.Values = append(page.Values, values[index])
			pageBytes += len(encoded) + 1
			page.NextOffset = index + 1
		}
		page.HasMore = page.NextOffset < len(values)
		return page, nil
	case "approval.decide", "approval.resolve_deferred":
		return nil, errors.New("worker API cannot make human approval decisions")
	case "handoff.question_answer.path":
		if !s.usageAllowed || s.questionAnswerID == "" || s.questionAnswerPath == "" {
			return nil, errors.New("controller has no prepared clarification answer for this invocation")
		}
		if c.RunID != s.scope.RunID || c.A != s.questionAnswerID {
			return nil, errors.New("clarification answer approval identity mismatch")
		}
		if s.questionAnswerMount == nil || s.questionAnswerMountInfo == nil {
			return nil, errors.New("controller clarification input mount identity is unavailable")
		}
		if err := validateQuestionAnswerMount(s.scope.TargetDir, s.scope.RunID, *s.questionAnswerMount); err != nil {
			return nil, fmt.Errorf("validate admitted clarification input mount: %w", err)
		}
		switch s.scope.Operation {
		case OperationResume, OperationRecover:
		default:
			return nil, fmt.Errorf("worker API clarification input is not allowed for operation %q", s.scope.Operation)
		}
		value, err := s.approvals.Load(s.scope.RunID, s.questionAnswerID)
		if err != nil {
			return nil, fmt.Errorf("reload durable clarification approval: %w", err)
		}
		if err := pipeline.ValidateQuestionAnswerApproval(value, s.questionAnswerReplay); err != nil {
			return nil, fmt.Errorf("validate durable clarification approval: %w", err)
		}
		if err := validateQuestionAnswerCandidate(context.Background(), s.questionAnswerStore.TargetDir, value); err != nil {
			return nil, err
		}
		path, err := s.questionAnswerStore.ValidateCanonicalQuestionAnswer(value)
		if err != nil {
			return nil, err
		}
		if path != s.questionAnswerMount.SourcePath {
			return nil, errors.New("canonical clarification source changed after worker admission")
		}
		return s.questionAnswerPath, nil
	case "lifecycle.create":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.Lifecycle.RunID != s.scope.RunID || c.Lifecycle.TargetDir != s.scope.TargetDir || c.Lifecycle.TargetDir == "" {
			return nil, errors.New("lifecycle create identity mismatch")
		}
		return nil, s.lifecycle.Create(c.Lifecycle)
	case "lifecycle.load":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.A != s.scope.RunID {
			return nil, errors.New("lifecycle load run mismatch")
		}
		state, err := s.lifecycle.Load(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if state.RunID != s.scope.RunID || state.TargetDir != s.scope.TargetDir {
			return nil, errors.New("lifecycle store returned another run or target")
		}
		return state, nil
	case "lifecycle.save":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.Previous.RunID != s.scope.RunID || c.Lifecycle.RunID != s.scope.RunID ||
			c.Previous.TargetDir != s.scope.TargetDir || c.Lifecycle.TargetDir != s.scope.TargetDir || c.Previous.TargetDir == "" {
			return nil, errors.New("lifecycle save identity mismatch")
		}
		current, err := s.lifecycle.Load(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if current.RunID != s.scope.RunID || current.TargetDir != s.scope.TargetDir {
			return nil, errors.New("lifecycle store returned another run or target")
		}
		if !sameLifecycleState(current, c.Previous) {
			return nil, errors.New("stale lifecycle state: previous checkpoint does not match controller state")
		}
		return nil, s.lifecycle.Save(c.Previous, c.Lifecycle)
	case "recorder.run_started":
		s.recorder.RunStarted(s.scope.RunID, c.A, c.B, c.At)
	case "recorder.run_resumed":
		s.recorder.RunResumed(s.scope.RunID, c.At)
	case "recorder.run_attached":
		s.recorder.RunAttached(s.scope.RunID)
	case "recorder.run_paused":
		s.recorder.RunPaused(s.scope.RunID, c.A, c.At)
	case "recorder.run_canceled":
		s.recorder.RunCanceled(s.scope.RunID, c.At)
	case "recorder.approval_requested":
		s.recorder.ApprovalRequested(s.scope.RunID, c.A, c.B, c.At, c.Data)
	case "recorder.transition_selected":
		s.recorder.TransitionSelected(s.scope.RunID, c.A, c.At, c.Data)
	case "recorder.stage_started":
		s.recorder.StageStarted(s.scope.RunID, c.A, c.B, c.Index, c.At)
	case "recorder.stage_finished":
		c.Stage.RunID = s.scope.RunID
		if c.Error != "" {
			c.Stage.Err = errors.New(c.Error)
		}
		s.recorder.StageFinished(c.Stage)
	case "recorder.attempts_invalidated":
		s.recorder.AttemptsInvalidated(s.scope.RunID, c.IDs, c.At)
	case "recorder.run_finished":
		s.recorder.RunFinished(s.scope.RunID, c.A, c.At)
	default:
		return nil, fmt.Errorf("worker API method %q is not allowed", method)
	}
	return nil, nil
}

func (s *workerAPIServer) validateControllerDescriptionMissing(event evidence.Event, events []evidence.Event) error {
	if event.Type != "description_missing" || s.approvals == nil {
		return errors.New("controller description event requires an approval-backed event")
	}
	approvalID, _ := event.Data["approval_id"].(string)
	value, err := s.approvals.Load(s.scope.RunID, approvalID)
	if err != nil {
		return fmt.Errorf("load human input event authority: %w", err)
	}
	if value.RunID != s.scope.RunID || value.ID != approvalID || value.Kind != approval.KindInput ||
		value.Trigger != "human_input" || value.FromStage != event.Stage || value.ToStage != event.Stage ||
		value.Status != approval.StatusResolved || value.ResolvedAction == "" || value.ResolvedAction == "reject" ||
		len(value.Decisions) == 0 {
		return errors.New("description_missing does not match a resolved human input approval")
	}
	var payload approval.InputPayload
	if json.Unmarshal(value.Payload, &payload) != nil || payload.StageID != event.Stage ||
		(payload.Result != "md" && payload.Result != "link" && payload.Result != "approve") {
		return errors.New("description_missing approval payload does not match a human result")
	}
	var started *evidence.Event
	for index := range events {
		candidate := &events[index]
		if candidate.Type == "attempt_started" && candidate.AttemptID == event.AttemptID && candidate.Stage == event.Stage {
			started = candidate
		}
	}
	if started == nil || started.Data["executor"] != "human" || started.Data["human_input_approval_id"] != approvalID {
		return errors.New("description_missing does not match a started human attempt")
	}
	actorID, _ := started.Data["actor_id"].(string)
	actorRole, _ := started.Data["actor_role"].(string)
	decision := value.Decisions[len(value.Decisions)-1]
	if actorID == "" || actorRole == "" || decision.ActorID != actorID || decision.ActorRole != actorRole ||
		decision.Action != value.ResolvedAction || strings.TrimSpace(decision.Description) != "" {
		return errors.New("description_missing does not match the resolved human input decision")
	}
	return nil
}

func (s *workerAPIServer) validateHumanSubmissionManifest(manifest evidence.AttemptManifest) error {
	if s.approvals == nil || manifest.Executor != "human" || manifest.HumanSubmissionVersion < 1 || manifest.HumanInputApprovalID == "" {
		return errors.New("human submission manifest requires its controller approval")
	}
	value, err := s.approvals.Load(s.scope.RunID, manifest.HumanInputApprovalID)
	if err != nil {
		return fmt.Errorf("load human submission manifest approval: %w", err)
	}
	if value.RunID != s.scope.RunID || value.ID != manifest.HumanInputApprovalID || value.Kind != approval.KindInput ||
		value.Trigger != "human_input" || value.FromStage != manifest.Stage || value.ToStage != manifest.Stage ||
		value.Status != approval.StatusResolved || len(value.Decisions) == 0 {
		return errors.New("human submission manifest does not match a resolved stage input")
	}
	var payload approval.InputPayload
	if json.Unmarshal(value.Payload, &payload) != nil || payload.StageID != manifest.Stage || payload.Result != manifest.HumanSubmissionResult ||
		payload.LinkKind != manifest.HumanSubmissionLinkKind {
		return errors.New("human submission manifest result does not match the configured input")
	}
	decision := value.Decisions[len(value.Decisions)-1]
	if decision.ActorID != manifest.ActorID || decision.ActorRole != manifest.ActorRole || decision.Action == "reject" ||
		decision.SubmissionVersion != manifest.HumanSubmissionVersion || decision.ContentSHA256 != manifest.HumanSubmissionSHA256 ||
		decision.Description != manifest.HumanSubmissionDescription {
		return errors.New("human submission manifest version/hash does not match its approval decision")
	}
	if decision.Action != "submit" && decision.Action != "approve" {
		return errors.New("human submission manifest has an unsupported resolved action")
	}
	if len(manifest.Outputs) != 1 {
		return errors.New("human submission manifest must bind exactly one output")
	}
	output := manifest.Outputs[0]
	if output.Type != "file" || output.Name != payload.OutputName || output.EvidencePath == "" {
		return errors.New("human submission manifest output does not match the configured contract")
	}
	outputPrefix := path.Join("attempts", manifest.AttemptID, "artifacts") + "/"
	if !strings.HasPrefix(output.EvidencePath, outputPrefix) || path.Clean(output.EvidencePath) != output.EvidencePath {
		return errors.New("human submission manifest output is outside its immutable attempt artifacts")
	}
	var expected []byte
	switch manifest.HumanSubmissionResult {
	case "md", "link":
		if decision.Action != "submit" {
			return errors.New("human submission manifest result requires submit action")
		}
		expected = []byte(decision.Comment)
	case "approve":
		if decision.Action != "approve" {
			return errors.New("human approval manifest result requires approve action")
		}
		expected, err = humanartifact.ApprovalResultContent(manifest.Stage, decision)
		if err != nil {
			return fmt.Errorf("encode human approval result: %w", err)
		}
	default:
		return errors.New("human submission manifest result is unsupported")
	}
	expectedSHA256 := humanartifact.Digest(expected)
	if output.Size != int64(len(expected)) || output.SHA256 != expectedSHA256 {
		return errors.New("human submission manifest output does not match its resolved decision bytes")
	}
	if strings.TrimSpace(s.scope.TargetDir) == "" {
		return errors.New("human submission manifest artifact root is unavailable")
	}
	runDir := filepath.Join(s.scope.TargetDir, ".ai-team", "runs", s.scope.RunID)
	artifactType, artifactSize, artifactSHA256, err := evidence.ArtifactDigestAt(runDir, output.EvidencePath)
	if err != nil || artifactType != "file" || artifactSize != int64(len(expected)) || artifactSHA256 != expectedSHA256 {
		return errors.New("human submission manifest attempt artifact does not match its resolved decision bytes")
	}
	return nil
}

func (s *workerAPIServer) validateWorkerDeliveryEvent(event evidence.Event, prior []evidence.Event, runDir string) error {
	switch event.Type {
	case "delivery_plan_approved":
		planHash, _ := event.Data["plan_hash"].(string)
		mode, _ := event.Data["mode"].(string)
		if mode == "hash_flag" {
			if s.approvedPlanHash == "" || planHash != s.approvedPlanHash {
				return errors.New("delivery plan approval does not match the controller-approved plan hash")
			}
			return nil
		}
		if mode != "resolved_approval" {
			return errors.New("delivery plan approval mode is not supported")
		}
		values, err := s.approvals.List(s.scope.RunID)
		if err != nil {
			return fmt.Errorf("list delivery approval authority: %w", err)
		}
		for _, value := range values {
			if value.Trigger == "delivery_plan" && value.SubjectHash == planHash && value.Status == approval.StatusResolved &&
				value.ResolvedAction == "approve" && value.RunID == s.scope.RunID && value.AttemptID == event.AttemptID &&
				hasAuthenticatedControllerDecision(s.approvals, value) {
				return nil
			}
		}
		return errors.New("delivery plan approval has no matching resolved controller approval")
	case "delivery_deferred":
		planHash, _ := event.Data["plan_hash"].(string)
		statePath, _ := event.Data["state_path"].(string)
		if !evidence.ValidDeliveryStatePath(runDir, statePath) {
			return errors.New("deferred delivery state path is outside the prepared delivery directory")
		}
		started := false
		finished := false
		approvedEvent := false
		for _, value := range prior {
			if value.AttemptID != event.AttemptID {
				continue
			}
			switch value.Type {
			case "attempt_started":
				started = true
			case "attempt_finished", "attempt_abandoned":
				finished = true
			case "delivery_plan_approved":
				approvedHash, _ := value.Data["plan_hash"].(string)
				if approvedHash == planHash {
					approvedEvent = true
				}
			}
		}
		if !started || finished {
			return errors.New("deferred delivery event must belong to the current unfinished controller attempt")
		}
		if !approvedEvent {
			return fmt.Errorf("deferred delivery plan %s has no matching approval event", planHash)
		}
		if s.approvedPlanHash == planHash {
			return nil
		}
		values, err := s.approvals.List(s.scope.RunID)
		if err != nil {
			return fmt.Errorf("list deferred delivery approval authority: %w", err)
		}
		for _, value := range values {
			if value.Trigger == "delivery_plan" && value.SubjectHash == planHash && value.Status == approval.StatusResolved &&
				value.ResolvedAction == "approve" && value.RunID == s.scope.RunID && value.AttemptID == event.AttemptID &&
				hasAuthenticatedControllerDecision(s.approvals, value) {
				return nil
			}
		}
		return fmt.Errorf("deferred delivery plan %s has no matching controller approval authority", planHash)
	case "deferred_gates_ratified":
		raw, ok := event.Data["gates"].([]any)
		if !ok || len(raw) == 0 {
			return errors.New("deferred gate ratification requires controller approvals")
		}
		values, err := s.approvals.List(s.scope.RunID)
		if err != nil {
			return fmt.Errorf("list deferred approval authority: %w", err)
		}
		for _, rawGate := range raw {
			gate, ok := rawGate.(map[string]any)
			if !ok {
				return errors.New("deferred gate ratification contains invalid gate")
			}
			id, _ := gate["approval_id"].(string)
			subjectHash, _ := gate["subject_hash"].(string)
			action, _ := gate["action"].(string)
			matched := false
			for _, value := range values {
				if value.ID == id && value.SubjectHash == subjectHash && value.Status == approval.StatusResolved && value.ResolvedAction == action {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("deferred gate %s does not match controller approval state", id)
			}
		}
	}
	return nil
}

func hasAuthenticatedControllerDecision(store any, value approval.PendingApproval) bool {
	trusted, ok := store.(approval.TrustedDecisionAuthority)
	return ok && trusted.HasAuthenticatedControllerDecision(value)
}

// validateControllerDeliveryClaims keeps event journals from gaining delivery
// authority merely by migration into controller storage. Resume, Recover, and
// Cancel correlate each historical approval marker with current controller
// state before the canonical copy is prepared. Cancel admission can still fail
// later (for example, because lifecycle state is already terminal), so it
// cannot bless an unverified delivery claim as canonical.
func validateControllerDeliveryClaims(job Job, events []evidence.Event, approvals interface {
	List(string) ([]approval.PendingApproval, error)
}) error {
	approvedPlanHash := strings.ToLower(strings.TrimSpace(job.ApprovePlanHash))
	approved := make(map[string]map[string]bool)
	var values []approval.PendingApproval
	valuesLoaded := false
	for _, event := range events {
		switch event.Type {
		case "delivery_plan_approved":
			planHash, _ := event.Data["plan_hash"].(string)
			mode, _ := event.Data["mode"].(string)
			if event.AttemptID == "" || planHash == "" {
				return errors.New("delivery plan approval event lacks attempt or plan identity")
			}
			switch mode {
			case "hash_flag":
				if approvedPlanHash == "" || approvedPlanHash != planHash {
					return fmt.Errorf("legacy hash-flag delivery claim %s has no matching controller job approval", planHash)
				}
			case "resolved_approval":
				if !valuesLoaded {
					if approvals == nil {
						return errors.New("controller approval authority is unavailable for legacy delivery claim")
					}
					var listErr error
					values, listErr = approvals.List(job.RunID)
					if listErr != nil {
						return fmt.Errorf("list legacy delivery approval authority: %w", listErr)
					}
					valuesLoaded = true
				}
				matched := false
				for _, value := range values {
					if value.RunID == job.RunID && value.Trigger == "delivery_plan" && value.SubjectHash == planHash &&
						value.AttemptID == event.AttemptID && value.Status == approval.StatusResolved && value.ResolvedAction == "approve" &&
						hasAuthenticatedControllerDecision(approvals, value) {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("legacy resolved delivery claim %s has no exact controller approval", planHash)
				}
			default:
				return fmt.Errorf("legacy delivery approval mode %q is unsupported", mode)
			}
			if approved[event.AttemptID] == nil {
				approved[event.AttemptID] = make(map[string]bool)
			}
			approved[event.AttemptID][planHash] = true
		case "delivery_deferred":
			planHash, _ := event.Data["plan_hash"].(string)
			if event.AttemptID == "" || planHash == "" || !approved[event.AttemptID][planHash] {
				return errors.New("legacy delivery_deferred has no preceding controller-authorized plan for the same attempt")
			}
		}
	}
	return nil
}

func (s *workerAPIServer) validateWorkerApprovalEvent(event evidence.Event) error {
	if event.Type != "approval_requested" && event.Type != "approval_decided" && event.Type != "approval_reused" {
		return nil
	}
	approvalID, ok := event.Data["approval_id"].(string)
	if !ok || approvalID == "" {
		return errors.New("approval event requires approval_id")
	}
	value, err := s.approvals.Load(s.scope.RunID, approvalID)
	if err != nil {
		return fmt.Errorf("load approval event authority: %w", err)
	}
	subjectHash, _ := event.Data["subject_hash"].(string)
	if value.RunID != s.scope.RunID || value.ID != approvalID || value.SubjectHash != subjectHash {
		return errors.New("approval event identity does not match controller approval state")
	}
	switch event.Type {
	case "approval_requested":
		status, _ := event.Data["status"].(string)
		// The pipeline persists the pending record before appending this event.
		// A web decision can race in that small interval, so the controller may
		// already hold the matching resolved record when the request event arrives.
		if value.AttemptID != event.AttemptID || status != string(approval.StatusPending) ||
			(value.Status != approval.StatusPending && value.Status != approval.StatusResolved) {
			return errors.New("approval_requested does not match a pending or just-resolved controller approval")
		}
	case "approval_decided":
		status, _ := event.Data["status"].(string)
		action, _ := event.Data["resolved_action"].(string)
		if value.AttemptID != event.AttemptID || value.Status != approval.StatusResolved || status != string(value.Status) || action == "" || action != value.ResolvedAction {
			return errors.New("approval_decided does not match a resolved controller approval")
		}
	case "approval_reused":
		priorStatus, _ := event.Data["prior_status"].(string)
		fromStage, _ := event.Data["from_stage"].(string)
		toStage, _ := event.Data["to_stage"].(string)
		trigger, _ := event.Data["trigger"].(string)
		if priorStatus != string(value.Status) || fromStage != value.FromStage || toStage != value.ToStage || trigger != value.Trigger {
			return errors.New("approval_reused does not match the prior controller approval")
		}
		if event.AttemptID == "" {
			return errors.New("approval_reused requires the current attempt identity")
		}
	}
	return nil
}

func (s *workerAPIServer) validateWorkerTerminalEvent(event evidence.Event) error {
	switch event.Type {
	case "run_canceled":
		if s.scope.Operation != OperationCancel {
			return errors.New("run_canceled is only allowed in a controller cancel operation")
		}
	case "run_finished":
		status, _ := event.Data["status"].(string)
		if status == string(workflow.RunCanceled) && s.scope.Operation != OperationCancel {
			return errors.New("canceled run_finished is only allowed in a controller cancel operation")
		}
		if s.scope.Operation == OperationCancel && status != string(workflow.RunCanceled) {
			return errors.New("controller cancel operation must finish as canceled")
		}
	}
	return nil
}

func candidateEvidencePath(root, runID, name string) (string, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return "", fmt.Errorf("candidate evidence run id: %w", err)
	}
	if name != "review-candidate.json" && name != "verification-candidate.json" {
		return "", fmt.Errorf("unsupported candidate evidence name %q", name)
	}
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("candidate evidence controller root must be absolute and clean")
	}
	return filepath.Join(root, runID, name), nil
}

// Candidate evidence writes are immutable. Serialize the check-and-create step
// process-wide so concurrent exact retries cannot both observe a missing file
// and have one fail after the other creates it. A single bounded mutex avoids
// retaining locks for every run ID.
var controllerCandidateEvidenceWriteMu sync.Mutex

func writeControllerCandidateEvidence(root, runID, name string, document pipeline.CandidateEvidence) error {
	controllerCandidateEvidenceWriteMu.Lock()
	defer controllerCandidateEvidenceWriteMu.Unlock()

	if err := pipeline.ValidateCandidateEvidence(runID, name, document); err != nil {
		return err
	}
	path, err := candidateEvidencePath(root, runID, name)
	if err != nil {
		return err
	}
	if data, readErr := safeio.ReadRegularFile(path, workerAPIMaxCandidateEvidenceBody); readErr == nil {
		var stored pipeline.CandidateEvidence
		if err := strictjson.Unmarshal(data, workerAPIMaxCandidateEvidenceBody, &stored); err != nil {
			return fmt.Errorf("stored candidate evidence is invalid: %w", err)
		}
		if err := pipeline.ValidateCandidateEvidence(runID, name, stored); err != nil {
			return err
		}
		if reflect.DeepEqual(stored, document) {
			return nil
		}
		return errors.New("candidate evidence already submitted with different content")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > workerAPIMaxCandidateEvidenceBody {
		return errors.New("candidate evidence exceeds controller storage limit")
	}
	if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
		return err
	}
	return safeio.WriteRegularFileNoFollow(path, append(data, '\n'), 0o600)
}

func readControllerCandidateEvidence(root, runID, name string) (pipeline.CandidateEvidence, error) {
	path, err := candidateEvidencePath(root, runID, name)
	if err != nil {
		return pipeline.CandidateEvidence{}, err
	}
	data, err := safeio.ReadRegularFile(path, workerAPIMaxCandidateEvidenceBody)
	if err != nil {
		return pipeline.CandidateEvidence{}, err
	}
	var document pipeline.CandidateEvidence
	if err := strictjson.Unmarshal(data, workerAPIMaxCandidateEvidenceBody, &document); err != nil {
		return pipeline.CandidateEvidence{}, err
	}
	if err := pipeline.ValidateCandidateEvidence(runID, name, document); err != nil {
		return pipeline.CandidateEvidence{}, err
	}
	return document, nil
}

// sameLifecycleState compares the complete persisted checkpoint. time.Time.Equal
// avoids treating equivalent instants with different location metadata as a
// stale checkpoint after a JSON round trip.
func sameLifecycleState(a, b lifecycle.State) bool {
	return a.SchemaVersion == b.SchemaVersion && a.RunID == b.RunID &&
		a.Feature == b.Feature && a.TargetDir == b.TargetDir && a.Task == b.Task &&
		a.Phase == b.Phase && a.NextStage == b.NextStage &&
		a.PendingApprovalID == b.PendingApprovalID && a.AttemptOrdinal == b.AttemptOrdinal &&
		a.ConfigSHA256 == b.ConfigSHA256 && a.WorkflowSHA256 == b.WorkflowSHA256 &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

type workerAPIPort struct {
	address, token string
	socketPath     string
	scope          workerAPIScope
	client         *http.Client
}

type WorkerAPIPort = workerAPIPort

func newWorkerAPIPort(job Job) (*workerAPIPort, error) {
	if err := evidence.ValidateRunID(job.RunID); err != nil {
		return nil, fmt.Errorf("worker controller API run id: %w", err)
	}
	address, okA := os.LookupEnv(workerAPIAddressEnv)
	token, okT := os.LookupEnv(workerAPITokenEnv)
	socketPath, hasSocket := os.LookupEnv(workerAPISocketEnv)
	if !okA || !okT || token == "" {
		return nil, errors.New("worker controller API environment is missing or invalid")
	}
	// Large event append envelopes can contain several MiB of escaped runtime
	// diagnostics; keep the transport bounded by the same TTL as the signed
	// request instead of timing out before the server can validate the payload.
	client := &http.Client{Timeout: workerAPIRequestTTL}
	switch {
	case strings.HasPrefix(address, "http://127.0.0.1:") && !hasSocket:
		// Compatibility transport for non-bubblewrap worker launches.
	case address == "http://unix" && hasSocket && filepath.IsAbs(socketPath) && filepath.Clean(socketPath) == socketPath:
		client.Transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
	default:
		return nil, errors.New("worker controller API environment is missing or invalid")
	}
	return &workerAPIPort{address: address, token: token, socketPath: socketPath, scope: workerAPIScope{RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, TargetDir: job.TargetDir}, client: client}, nil
}

func NewWorkerAPIPort(job Job) (*WorkerAPIPort, error) { return newWorkerAPIPort(job) }

// SupportsControllerUsageStore reports whether this worker API uses the Unix
// transport that is reachable only from the bubblewrap worker namespace.
func (p *workerAPIPort) SupportsControllerUsageStore() bool {
	return p != nil && p.address == "http://unix" && p.socketPath != ""
}

func (p *workerAPIPort) SupportsControllerAttestationStore() bool {
	return p != nil && p.SupportsControllerUsageStore()
}

func (p *workerAPIPort) SupportsControllerContainmentStore() bool {
	return p != nil && p.SupportsControllerUsageStore()
}

type workerAPIAttestationWriter struct{ port *workerAPIPort }
type WorkerAPIAttestationWriter = workerAPIAttestationWriter

func NewWorkerAPIAttestationWriter(port *WorkerAPIPort) pipeline.AttestationWriter {
	return &workerAPIAttestationWriter{port: port}
}
func (w *workerAPIAttestationWriter) WriteAttestation(statement *attest.Statement) error {
	if w == nil || w.port == nil || !w.port.SupportsControllerContainmentStore() {
		return errors.New("worker attestation API unavailable")
	}
	if statement == nil || statement.Predicate.RunID != w.port.scope.RunID {
		return errors.New("worker attestation API run mismatch")
	}
	return w.port.call("attestation.write", workerAPICall{Attestation: *statement}, nil)
}

type workerAPIContainmentReceiptWriter struct{ port *workerAPIPort }
type WorkerAPIContainmentReceiptWriter = workerAPIContainmentReceiptWriter

func NewWorkerAPIContainmentReceiptWriter(port *WorkerAPIPort) pipeline.ContainmentReceiptWriter {
	return &workerAPIContainmentReceiptWriter{port: port}
}

func (w *workerAPIContainmentReceiptWriter) WriteContainmentReceipt(receipt containment.Receipt) error {
	if w == nil || w.port == nil || !w.port.SupportsControllerAttestationStore() {
		return errors.New("worker containment API unavailable")
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	return w.port.call("containment.write", workerAPICall{ContainmentReceipt: &receipt}, nil)
}

type workerAPITerminalRecordWriter struct{ port *workerAPIPort }
type WorkerAPITerminalRecordWriter = workerAPITerminalRecordWriter

func NewWorkerAPITerminalRecordWriter(port *WorkerAPIPort) pipeline.TerminalRecordWriter {
	return &workerAPITerminalRecordWriter{port: port}
}
func (w *workerAPITerminalRecordWriter) WriteTerminalRecord(record delivery.TerminalRecord) error {
	if w == nil || w.port == nil || w.port.address != "http://unix" {
		return errors.New("worker terminal delivery API unavailable")
	}
	if record.RunID != w.port.scope.RunID {
		return errors.New("worker terminal delivery API run mismatch")
	}
	return w.port.call("delivery.terminal_record.write", workerAPICall{TerminalRecord: record}, nil)
}

func (p *workerAPIPort) call(method string, value, out any) error {
	return p.callWithRandom(method, value, out, rand.Reader)
}

func (p *workerAPIPort) callWithRandom(method string, value, out any, random io.Reader) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var nonceBytes [32]byte
	if _, err := io.ReadFull(random, nonceBytes[:]); err != nil {
		return err
	}
	data, err := json.Marshal(workerAPIRequest{workerAPIScope: p.scope, Method: method, Payload: payload, Nonce: hex.EncodeToString(nonceBytes[:]), IssuedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, p.address+"/v1/call", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	responseLimit := workerAPIMaxBody
	if method == "candidate.evidence.read" || method == "attempt_manifest.read" {
		responseLimit = workerAPIMaxCandidateEvidenceBody
	}
	if method == "attempt_manifest.read" {
		responseLimit = workerAPIMaxAttemptManifestEnvelope
	}
	if method == "approval.load" || method == "approval.list" {
		responseLimit = workerAPIMaxApprovalReadResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(responseLimit)+1))
	if err != nil {
		return err
	}
	if len(body) > responseLimit {
		return errors.New("worker API response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return workerAPIResponseError{status: resp.StatusCode, message: fmt.Sprintf("worker API %s: %s", method, strings.TrimSpace(string(body)))}
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

type workerAPIAttemptManifestStore struct{ port *workerAPIPort }
type WorkerAPIAttemptManifestStore = workerAPIAttemptManifestStore

type workerAPIEventLog struct {
	port *workerAPIPort
	mu   sync.Mutex
}

type WorkerAPIEventLog = workerAPIEventLog

func NewWorkerAPIEventLog(port *WorkerAPIPort) evidence.EventLog {
	return &workerAPIEventLog{port: port}
}

func (*workerAPIEventLog) ExternalEventAuthority() {}

func (s *workerAPIEventLog) Read(runID string) ([]evidence.Event, error) {
	data, err := s.ReadBytes(runID)
	if err != nil {
		return nil, err
	}
	return evidence.VerifyEventLogBytes(data, runID)
}

func (s *workerAPIEventLog) ReadBytes(runID string) ([]byte, error) {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return nil, errors.New("worker event log API unavailable")
	}
	if runID != s.port.scope.RunID {
		return nil, errors.New("worker event log API run mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []byte
	var snapshot string
	var offset int64
	var total int64 = -1
	for {
		var page workerAPIEventPage
		if err := s.port.call("event_log.read", workerAPICall{
			RunID: runID, Offset: offset, Snapshot: snapshot,
		}, &page); err != nil {
			return nil, err
		}
		if page.Offset != offset || page.Total < 0 || page.Total > evidence.MaxEventLogSize || int64(len(page.Data)) > page.Total-offset || len(page.Data) > workerAPIEventReadPage || page.Snapshot == "" || (snapshot != "" && page.Snapshot != snapshot) {
			return nil, errors.New("worker event log API returned an invalid page identity")
		}
		if total < 0 {
			total = page.Total
			result = make([]byte, 0, total)
		} else if total != page.Total {
			return nil, errors.New("worker event log API changed snapshot size")
		}
		if len(page.Data) == 0 && offset < total {
			return nil, errors.New("worker event log API returned an empty page before end")
		}
		result = append(result, page.Data...)
		offset += int64(len(page.Data))
		snapshot = page.Snapshot
		if offset == total {
			break
		}
	}
	if _, err := evidence.VerifyEventLogBytes(result, runID); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *workerAPIEventLog) Append(runID string, event evidence.Event, expectedSequence uint64, expectedPreviousSHA256 string) (evidence.Event, error) {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return evidence.Event{}, errors.New("worker event log API unavailable")
	}
	if runID != s.port.scope.RunID {
		return evidence.Event{}, errors.New("worker event log API run mismatch")
	}
	var result workerAPIEventAppendResult
	err := s.port.call("event_log.append", workerAPICall{
		RunID: runID, Event: event, ExpectedSequence: expectedSequence,
		ExpectedPreviousSHA256: expectedPreviousSHA256,
	}, &result)
	if err != nil {
		return evidence.Event{}, err
	}
	event.SchemaVersion = result.SchemaVersion
	event.Sequence = result.Sequence
	event.RunID = result.RunID
	event.Type = result.Type
	event.Stage = result.Stage
	event.AttemptID = result.AttemptID
	event.Timestamp = result.Timestamp
	event.PreviousSHA256 = result.PreviousSHA256
	event.SHA256 = result.SHA256
	return event, nil
}

func (s *workerAPIEventLog) AppendControllerEvent(runID string, event evidence.Event, expectedSequence uint64, expectedPreviousSHA256 string) (evidence.Event, error) {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return evidence.Event{}, errors.New("worker event log API unavailable")
	}
	if runID != s.port.scope.RunID || event.Type != "description_missing" {
		return evidence.Event{}, errors.New("worker controller event log API identity or type mismatch")
	}
	var result workerAPIEventAppendResult
	if err := s.port.call("event_log.description_missing", workerAPICall{
		RunID: runID, Event: event, ExpectedSequence: expectedSequence,
		ExpectedPreviousSHA256: expectedPreviousSHA256,
	}, &result); err != nil {
		return evidence.Event{}, err
	}
	event.SchemaVersion, event.Sequence, event.RunID = result.SchemaVersion, result.Sequence, result.RunID
	event.Type, event.Stage, event.AttemptID = result.Type, result.Stage, result.AttemptID
	event.Timestamp, event.PreviousSHA256, event.SHA256 = result.Timestamp, result.PreviousSHA256, result.SHA256
	return event, nil
}

// NewWorkerAPIAttemptManifestStore exposes the scoped controller-owned
// attempt-manifest read/write port to pipeline workers.
func NewWorkerAPIAttemptManifestStore(port *WorkerAPIPort) *WorkerAPIAttemptManifestStore {
	return &workerAPIAttemptManifestStore{port: port}
}

func (s *workerAPIAttemptManifestStore) WriteAttemptManifest(manifest evidence.AttemptManifest) error {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return errors.New("worker attempt manifest API unavailable")
	}
	if manifest.RunID != "" && manifest.RunID != s.port.scope.RunID {
		return errors.New("worker attempt manifest API run mismatch")
	}
	return s.port.call("attempt_manifest.write", workerAPICall{AttemptManifest: manifest}, nil)
}

func (s *workerAPIAttemptManifestStore) ReadAttemptManifest(_ string, runID, attemptID string) ([]byte, error) {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return nil, errors.New("worker attempt manifest API unavailable")
	}
	if runID != s.port.scope.RunID {
		return nil, errors.New("worker attempt manifest API run mismatch")
	}
	var data []byte
	if err := s.port.call("attempt_manifest.read", workerAPICall{A: attemptID}, &data); err != nil {
		return nil, err
	}
	return data, nil
}

type workerAPIRecorder struct {
	port     *workerAPIPort
	mu       sync.Mutex
	firstErr error
}
type WorkerAPIRecorder = workerAPIRecorder

func NewWorkerAPIRecorder(port *WorkerAPIPort) pipeline.Recorder {
	return &workerAPIRecorder{port: port}
}
func (r *workerAPIRecorder) send(method string, c workerAPICall) {
	if err := r.port.call("recorder."+method, c, nil); err != nil {
		r.mu.Lock()
		if r.firstErr == nil {
			r.firstErr = err
			// pipeline.Recorder intentionally has no error return. Preserve the
			// first infrastructure failure in captured child diagnostics so the
			// parent ProcessEngine can fail the invocation closed.
			_, _ = fmt.Fprintf(os.Stderr, "%s%s\n", workerAPIErrorMarker, err)
		}
		r.mu.Unlock()
	}
}

func (r *workerAPIRecorder) Error() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstErr
}

func workerAPIRecorderFailure(diagnostics string) string {
	for _, line := range strings.Split(diagnostics, "\n") {
		if index := strings.Index(line, workerAPIErrorMarker); index >= 0 {
			return strings.TrimSpace(line[index+len(workerAPIErrorMarker):])
		}
	}
	return ""
}

// Reconciliation is controller startup maintenance, never a worker report.
func (r *workerAPIRecorder) ReconcileInterrupted(time.Time) {}
func (r *workerAPIRecorder) RunStarted(id, feature, snapshot string, at time.Time) {
	r.send("run_started", workerAPICall{A: feature, B: snapshot, At: at})
}
func (r *workerAPIRecorder) RunResumed(_ string, at time.Time) {
	r.send("run_resumed", workerAPICall{At: at})
}
func (r *workerAPIRecorder) RunAttached(_ string) { r.send("run_attached", workerAPICall{}) }
func (r *workerAPIRecorder) RunPaused(_, status string, at time.Time) {
	r.send("run_paused", workerAPICall{A: status, At: at})
}
func (r *workerAPIRecorder) RunCanceled(_ string, at time.Time) {
	r.send("run_canceled", workerAPICall{At: at})
}
func (r *workerAPIRecorder) ApprovalRequested(_, id, attempt string, at time.Time, data map[string]any) {
	r.send("approval_requested", workerAPICall{A: id, B: attempt, At: at, Data: data})
}
func (r *workerAPIRecorder) ApprovalDecided(_, id, attempt string, at time.Time, data map[string]any) {
	// Human decisions are committed by the controller approval service. The
	// worker-side event cannot assert or manufacture that decision.
}
func (r *workerAPIRecorder) TransitionSelected(_, attempt string, at time.Time, data map[string]any) {
	r.send("transition_selected", workerAPICall{A: attempt, At: at, Data: data})
}
func (r *workerAPIRecorder) StageStarted(_, attempt, agent string, index int, at time.Time) {
	r.send("stage_started", workerAPICall{A: attempt, B: agent, Index: index, At: at})
}
func (r *workerAPIRecorder) StageFinished(stage notifier.StageResult) {
	message := ""
	if stage.Err != nil {
		message = stage.Err.Error()
	}
	stage.Err = nil
	r.send("stage_finished", workerAPICall{Stage: stage, Error: message})
}
func (r *workerAPIRecorder) AttemptsInvalidated(_ string, ids []string, at time.Time) {
	r.send("attempts_invalidated", workerAPICall{IDs: ids, At: at})
}
func (r *workerAPIRecorder) RunFinished(_, status string, at time.Time) {
	r.send("run_finished", workerAPICall{A: status, At: at})
}

type workerAPIApprovals struct{ port *workerAPIPort }
type WorkerAPIApprovals = workerAPIApprovals

func NewWorkerAPIApprovals(port *WorkerAPIPort) pipeline.ApprovalStore {
	return &workerAPIApprovals{port: port}
}
func (a *workerAPIApprovals) Create(value approval.PendingApproval) (approval.PendingApproval, error) {
	var out approval.PendingApproval
	err := a.port.call("approval.create", workerAPICall{Approval: value}, &out)
	return out, err
}
func (a *workerAPIApprovals) Load(_, id string) (approval.PendingApproval, error) {
	var out approval.PendingApproval
	err := a.port.call("approval.load", workerAPICall{A: id}, &out)
	return out, err
}
func (a *workerAPIApprovals) List(_ string) ([]approval.PendingApproval, error) {
	var out []approval.PendingApproval
	offset := 0
	for {
		var page workerAPIApprovalListPage
		if err := a.port.call("approval.list", workerAPICall{Index: offset}, &page); err != nil {
			return nil, err
		}
		if page.NextOffset != offset+len(page.Values) ||
			(page.HasMore && page.NextOffset <= offset) || (!page.HasMore && page.NextOffset != offset+len(page.Values)) {
			return nil, errors.New("worker API approval list returned an invalid page")
		}
		out = append(out, page.Values...)
		offset = page.NextOffset
		if !page.HasMore {
			return out, nil
		}
	}
}
func (a *workerAPIApprovals) HasAuthenticatedControllerDecision(value approval.PendingApproval) bool {
	return a != nil && a.port != nil && controllerDecisionMarkedAuthenticated(value)
}

func controllerDecisionMarkedAuthenticated(value approval.PendingApproval) bool {
	if value.Status != approval.StatusResolved || len(value.Decisions) == 0 {
		return false
	}
	for _, decision := range value.Decisions {
		if !decision.ControllerAuthenticated || decision.ApprovalID != value.ID ||
			decision.SubjectHash != value.SubjectHash || decision.Action != value.ResolvedAction {
			return false
		}
	}
	return true
}
func (*workerAPIApprovals) Decide(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}
func (*workerAPIApprovals) ResolveDeferred(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}

var _ approval.TrustedDecisionAuthority = (*workerAPIApprovals)(nil)

type workerAPIBriefs struct{ port *workerAPIPort }
type WorkerAPIBriefs = workerAPIBriefs

func NewWorkerAPIBriefs(port *WorkerAPIPort) pipeline.BriefStore { return &workerAPIBriefs{port: port} }

func (b *workerAPIBriefs) checkRun(runID string) error {
	if b == nil || b.port == nil || runID == "" || runID != b.port.scope.RunID {
		return errors.New("worker brief API run mismatch")
	}
	return nil
}

type workerAPICandidates struct{ port *workerAPIPort }
type WorkerAPICandidates = workerAPICandidates

func NewWorkerAPICandidates(port *WorkerAPIPort) candidate.MetadataStore {
	return &workerAPICandidates{port: port}
}
func (c *workerAPICandidates) Create(metadata candidate.Metadata) error {
	if c == nil || c.port == nil {
		return errors.New("worker candidate metadata API unavailable")
	}
	target, err := candidate.CanonicalTargetDir(c.port.scope.TargetDir)
	if err != nil {
		return fmt.Errorf("worker candidate metadata target: %w", err)
	}
	if err := candidate.ValidateMetadata(target, c.port.scope.RunID, metadata); err != nil {
		return fmt.Errorf("worker candidate metadata identity mismatch (run=%q target=%q metadata=%+v): %w", c.port.scope.RunID, c.port.scope.TargetDir, metadata, err)
	}
	return c.port.call("candidate.metadata.create", workerAPICall{CandidateMetadata: metadata}, nil)
}
func (c *workerAPICandidates) Read(target, runID string) (candidate.Metadata, error) {
	if c == nil || c.port == nil || runID != c.port.scope.RunID {
		return candidate.Metadata{}, errors.New("worker candidate metadata scope mismatch")
	}
	canonicalTarget, err := candidate.CanonicalTargetDir(target)
	if err != nil {
		return candidate.Metadata{}, fmt.Errorf("worker candidate metadata target: %w", err)
	}
	canonicalScope, err := candidate.CanonicalTargetDir(c.port.scope.TargetDir)
	if err != nil || canonicalTarget != canonicalScope {
		return candidate.Metadata{}, errors.New("worker candidate metadata scope mismatch")
	}
	var result candidate.Metadata
	err = c.port.call("candidate.metadata.read", workerAPICall{A: runID}, &result)
	if err == nil {
		err = candidate.ValidateMetadata(canonicalTarget, runID, result)
	}
	return result, err
}

func (c *workerAPICandidates) MarkAbsent(string, string) error {
	return errors.New("worker candidate API cannot create candidate absence markers")
}

type workerAPICandidateEvidenceStore struct{ port *workerAPIPort }

type WorkerAPICandidateEvidenceStore = workerAPICandidateEvidenceStore

func NewWorkerAPICandidateEvidenceStore(port *WorkerAPIPort) pipeline.CandidateEvidenceStore {
	return &workerAPICandidateEvidenceStore{port: port}
}

func (s *workerAPICandidateEvidenceStore) check(name string) error {
	if s == nil || s.port == nil || !s.port.SupportsControllerUsageStore() {
		return errors.New("controller candidate evidence API unavailable")
	}
	if name != "review-candidate.json" && name != "verification-candidate.json" {
		return fmt.Errorf("unsupported candidate evidence name %q", name)
	}
	return nil
}

func (s *workerAPICandidateEvidenceStore) WriteCandidateEvidence(name string, document pipeline.CandidateEvidence) error {
	if err := s.check(name); err != nil {
		return err
	}
	if err := pipeline.ValidateCandidateEvidence(s.port.scope.RunID, name, document); err != nil {
		return err
	}
	return s.port.call("candidate.evidence.write", workerAPICall{
		RunID: s.port.scope.RunID, CandidateEvidenceName: name, CandidateEvidence: document,
	}, nil)
}

func (s *workerAPICandidateEvidenceStore) ReadCandidateEvidence(name string) (pipeline.CandidateEvidence, error) {
	if err := s.check(name); err != nil {
		return pipeline.CandidateEvidence{}, err
	}
	var document pipeline.CandidateEvidence
	err := s.port.call("candidate.evidence.read", workerAPICall{RunID: s.port.scope.RunID, CandidateEvidenceName: name}, &document)
	if err == nil {
		err = pipeline.ValidateCandidateEvidence(s.port.scope.RunID, name, document)
	}
	return document, err
}

var _ pipeline.CandidateEvidenceStore = (*workerAPICandidateEvidenceStore)(nil)

func (c *workerAPICandidates) ReadAbsent(target, runID string) error {
	if c == nil || c.port == nil || runID != c.port.scope.RunID {
		return errors.New("worker candidate absence API scope mismatch")
	}
	canonicalTarget, err := candidate.CanonicalTargetDir(target)
	if err != nil {
		return fmt.Errorf("worker candidate absence target: %w", err)
	}
	canonicalScope, err := candidate.CanonicalTargetDir(c.port.scope.TargetDir)
	if err != nil || canonicalTarget != canonicalScope {
		return errors.New("worker candidate absence API scope mismatch")
	}
	return c.port.call("candidate.absence.read", workerAPICall{A: runID}, nil)
}

func (b *workerAPIBriefs) CreateInitial(runID, intention string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.create_initial", workerAPICall{A: intention}, &result)
	return result, err
}

func (b *workerAPIBriefs) AppendClarification(runID, approvalID string, provenance pipeline.ClarificationProvenance, questions, answer string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	if strings.TrimSpace(provenance.Stage) == "" || strings.TrimSpace(provenance.ActorID) == "" || strings.TrimSpace(provenance.ActorRole) == "" {
		return pipeline.BriefDocument{}, errors.New("clarification provenance is required")
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.append_clarification", workerAPICall{A: approvalID, B: questions, C: answer}, &result)
	return result, err
}

func (b *workerAPIBriefs) List(runID string) ([]pipeline.BriefVersion, error) {
	if err := b.checkRun(runID); err != nil {
		return nil, err
	}
	var result []pipeline.BriefVersion
	err := b.port.call("brief.list", workerAPICall{}, &result)
	return result, err
}

func (b *workerAPIBriefs) Read(runID, versionID string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.read", workerAPICall{A: versionID}, &result)
	return result, err
}

type workerAPILifecycle struct{ port *workerAPIPort }
type WorkerAPILifecycle = workerAPILifecycle

func NewWorkerAPILifecycle(port *WorkerAPIPort) *WorkerAPILifecycle {
	return &workerAPILifecycle{port: port}
}
func (s *workerAPILifecycle) Create(state lifecycle.State) error {
	return s.port.call("lifecycle.create", workerAPICall{Lifecycle: state}, nil)
}
func (s *workerAPILifecycle) Load(runID string) (lifecycle.State, error) {
	if runID != s.port.scope.RunID {
		return lifecycle.State{}, errors.New("lifecycle load run mismatch")
	}
	var state lifecycle.State
	err := s.port.call("lifecycle.load", workerAPICall{A: runID}, &state)
	return state, err
}
func (s *workerAPILifecycle) Save(previous, next lifecycle.State) error {
	return s.port.call("lifecycle.save", workerAPICall{Previous: previous, Lifecycle: next}, nil)
}

type workerAPIUsageEnvelopeWriter struct{ port *workerAPIPort }
type WorkerAPIUsageEnvelopeWriter = workerAPIUsageEnvelopeWriter

func NewWorkerAPIUsageEnvelopeWriter(port *WorkerAPIPort) pipeline.UsageEnvelopeWriter {
	return &workerAPIUsageEnvelopeWriter{port: port}
}

func (w *workerAPIUsageEnvelopeWriter) WriteUsageEnvelope(envelope metrics.UsageEnvelope) error {
	if w == nil || w.port == nil {
		return errors.New("worker usage API unavailable")
	}
	if err := metrics.ValidateUsageEnvelope(w.port.scope.RunID, envelope); err != nil {
		return err
	}
	return w.port.call("usage.envelope.write", workerAPICall{Usage: envelope}, nil)
}

var _ pipeline.Recorder = (*workerAPIRecorder)(nil)
var _ pipeline.ApprovalStore = (*workerAPIApprovals)(nil)
var _ pipeline.BriefStore = (*workerAPIBriefs)(nil)
var _ candidate.MetadataStore = (*workerAPICandidates)(nil)
var _ lifecycle.StorePort = (*workerAPILifecycle)(nil)
var _ pipeline.UsageEnvelopeWriter = (*workerAPIUsageEnvelopeWriter)(nil)
