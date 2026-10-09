package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	agentdata "github.com/arturpanteleev/ai-team"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const maxArtifactSize = 10 << 20 // 10 MiB: web viewer не предназначен для больших бинарных файлов.
const maxLogTailSize = 64 << 10
const maxCommandBody = 64 << 10
const sessionCookieName = "ai_team_session"

type RunController interface {
	Start(feature, task string) (string, error)
	StartWithAdmission(feature, task string, admit func(runID string) error) (string, error)
	Resume(runID string) error
	Cancel(runID string) error
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
	Approvals(runID string) ([]approval.PendingApproval, error)
	DeliverDeferred(ctx context.Context, runID string) (delivery.TerminalRecord, error)
	Preflight(context.Context) preflight.Report
}

type ServerOption func(*Server)

func WithRunController(controller RunController) ServerOption {
	return func(server *Server) { server.controller = controller }
}

// WithTargetDir supplies the canonical project target independently of the
// configured artifact store path. Cloud callers should always set it because
// artifact storage may be relocated with --artifacts.
func WithTargetDir(target string) ServerOption {
	return func(server *Server) { server.targetDir = target }
}

// WithTemplateAgentLookup supplies the same project-aware agent registry used
// by execution, so editor validation cannot publish references workers cannot
// resolve.
func WithTemplateAgentLookup(lookup config.AgentLookup) ServerOption {
	return func(server *Server) { server.templateAgentLookup = lookup }
}

type IdentityVerifier interface {
	Verify(token string) (cloudidentity.Principal, error)
}

func WithAuthenticator(verifier IdentityVerifier) ServerOption {
	return func(server *Server) { server.authenticator = verifier }
}

// LocalAuthenticator verifies the bearer token generated for a loopback web
// server. Its identity and available workflow roles are fixed by the server.
type LocalAuthenticator struct {
	token     string
	principal cloudidentity.Principal
}

// NewLocalAuthenticator creates a verifier for a locally generated token.
func NewLocalAuthenticator(token string) (*LocalAuthenticator, error) {
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("local web token must contain at least 32 characters")
	}
	principal, err := cloudidentity.NewPrincipal("local-user", []cloudidentity.Role{
		cloudidentity.RoleProductOwner, cloudidentity.RoleArchitect,
		cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer,
		cloudidentity.RoleQA, cloudidentity.RoleReleaseManager,
	})
	if err != nil {
		return nil, err
	}
	return &LocalAuthenticator{token: token, principal: principal}, nil
}

func (a *LocalAuthenticator) Verify(token string) (cloudidentity.Principal, error) {
	if a == nil || !constantTimeEqual(token, a.token) {
		return cloudidentity.Principal{}, errors.New("invalid local web token")
	}
	return a.principal, nil
}

// WithLocalAuthenticator enables local mode, where decision roles are derived
// from the matching server-side approval rather than the request body.
func WithLocalAuthenticator(verifier *LocalAuthenticator) ServerOption {
	return func(server *Server) {
		server.authenticator = verifier
		server.localAuth = true
	}
}

type browserSession struct {
	CSRFToken    string
	Principal    cloudidentity.Principal
	SessionEpoch int64
	ExpiresAt    time.Time
}

type Server struct {
	store               *store.Store
	humanArtifacts      *humanartifact.Store
	hub                 *Hub
	router              *chi.Mux
	frontend            http.Handler
	artifactRoot        string // абсолютный корень артефактов; всё вне него не отдаётся
	targetDir           string // canonical project/control root; independent of artifactRoot
	runRoot             string // immutable .ai-team/runs root
	httpServer          *http.Server
	cancelEvents        context.CancelFunc
	eventWorkers        sync.WaitGroup
	controller          RunController
	templateAgentLookup config.AgentLookup
	templateMu          sync.Mutex
	authenticator       IdentityVerifier
	localAuth           bool
	sessions            map[string]browserSession
	sessionMu           sync.Mutex
	streamID            string
}

// NewServer создаёт web-сервер. artifactRoot — корень артефактов
// (обычно .ai-team/artifacts); файлы вне корня недоступны.
func NewServer(dbPath, distDir, artifactRoot string, options ...ServerOption) (*Server, error) {
	s, err := store.New(dbPath)
	if err != nil {
		return nil, err
	}

	absRoot, err := filepath.Abs(artifactRoot)
	if err != nil {
		// аварийный путь инициализации: значимая ошибка возвращается вызывающему.
		_ = s.Close()
		return nil, err
	}

	hub := NewHub()

	streamID, tokenErr := randomToken()
	if tokenErr != nil {
		_ = s.Close()
		return nil, fmt.Errorf("stream identity: %w", tokenErr)
	}
	srv := &Server{
		store:        s,
		hub:          hub,
		streamID:     streamID[:16],
		artifactRoot: absRoot,
		sessions:     make(map[string]browserSession),
	}
	for _, option := range options {
		option(srv)
	}
	target := srv.targetDir
	if target == "" {
		// Backward-compatible default for callers using the conventional
		// <target>/.ai-team/artifacts layout. Custom artifact roots must provide
		// WithTargetDir and are never used as a source of target identity.
		target = filepath.Dir(filepath.Dir(absRoot))
	}
	target, err = filepath.Abs(target)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("project target path: %w", err)
	}
	target, err = filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("project target path: %w", err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil || !targetInfo.IsDir() {
		_ = s.Close()
		return nil, errors.New("project target must be an existing directory")
	}
	srv.targetDir = filepath.Clean(target)
	srv.runRoot = filepath.Join(srv.targetDir, ".ai-team", "runs")
	srv.humanArtifacts, err = humanartifact.New(srv.targetDir)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("human artifact store: %w", err)
	}
	hub.SetReplay(srv.replayEvents)
	go hub.Run()
	eventContext, cancelEvents := context.WithCancel(context.Background())
	srv.cancelEvents = cancelEvents
	eventCursor, err := s.LatestEventCursor()
	if err != nil {
		cancelEvents()
		// аварийный путь инициализации: значимая ошибка возвращается вызывающему.
		_ = s.Close()
		return nil, fmt.Errorf("initial event cursor: %w", err)
	}
	srv.eventWorkers.Add(1)
	go srv.tailEvents(eventContext, eventCursor)

	srv.router = chi.NewRouter()
	srv.router.Use(middleware.Recoverer)
	if srv.authenticator == nil || srv.localAuth {
		srv.router.Use(sameOriginMiddleware)
	} else {
		srv.router.Use(authenticatedOriginMiddleware)
	}

	srv.router.Get("/api/auth/config", srv.handleAuthConfig)
	srv.router.Get("/api/session", srv.handleSession)
	srv.router.Post("/api/team/activate", srv.handleTeamActivation)
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.teamReadSecurity)
		router.Get("/api/team/members", srv.handleTeamMembers)
		router.Get("/api/team/audit", srv.handleTeamAudit)
	})
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.teamWriteSecurity)
		router.Post("/api/team/invitations", srv.handleTeamInvite)
		router.Patch("/api/team/members/{actorID}/roles", srv.handleTeamRoles)
		router.Delete("/api/team/members/{actorID}", srv.handleTeamRevoke)
	})
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.readSecurity)
		router.Get("/api/pipelines", srv.handleGetPipelines)
		router.Get("/api/pipelines/{id}", srv.handleGetPipeline)
		router.Get("/api/pipelines/{id}/artifacts", srv.handleGetArtifacts)
		router.Get("/api/artifacts/*", srv.handleGetArtifact)
		router.Get("/api/runs/{runID}/artifacts/*", srv.handleGetRunArtifact)
		router.Get("/api/runs/{runID}/artifact-revisions", srv.handleListArtifactRevisions)
		router.Get("/api/runs/{runID}/logs/{attemptID}", srv.handleGetRunLog)
		router.Get("/api/runs/{runID}/workflow", srv.handleGetRunWorkflow)
		router.Get("/api/runs/{runID}/template-version", srv.handleGetRunTemplateVersion)
		router.Get("/api/template", srv.handleGetTemplate)
		router.Get("/api/template/versions", srv.handleGetTemplateVersions)
		router.Get("/api/preflight", srv.handlePreflight)
		router.Get("/api/auth/me", srv.handleCurrentIdentity)
	})
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.writeSecurity)
		router.Post("/api/runs", srv.handleStartRun)
		router.Post("/api/template/validate", srv.handleValidateTemplate)
		router.Post("/api/template/publish", srv.handlePublishTemplate)
		router.Post("/api/runs/{runID}/resume", srv.handleResumeRun)
		router.Post("/api/runs/{runID}/delivery/retry", srv.handleRetryDelivery)
		router.Post("/api/runs/{runID}/cancel", srv.handleCancelRun)
		router.Post("/api/runs/{runID}/approvals/{approvalID}/decisions", srv.handleDecision)
		router.Post("/api/runs/{runID}/stages/{stageID}/take", srv.handleTakeStage)
		router.Post("/api/runs/{runID}/artifact-revisions", srv.handleCreateArtifactRevision)
	})
	srv.router.With(srv.readSecurity).Get("/ws", srv.handleWebSocket)

	// Оба промаха мимо роутов — по пути и по методу — сводятся в один
	// respondFallback, чтобы ответ не зависел от того, какую ветку выбрал chi
	// и собран ли фронтенд. Дефолтный MethodNotAllowedHandler chi здесь не
	// подходит: он берёт методы из неэкспортированного Context.methodsAllowed,
	// куда попадает и SPA-катч-олл `/*`, поэтому на API-путях обещал в Allow
	// метод GET, который после этого фикса отдаёт 404. respondFallback считает
	// методы сам через публичный Mux.Find и такого не обещает.
	srv.router.NotFound(srv.respondFallback)
	srv.router.MethodNotAllowed(srv.respondFallback)

	if distDir != "" {
		srv.frontend, err = frontendHandler(distDir)
		if err != nil {
			// аварийный путь инициализации: значимая ошибка возвращается вызывающему.
			_ = s.Close()
			return nil, err
		}
		srv.router.Get("/*", srv.handleFrontend)
	}

	return srv, nil
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "run controller unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	report := s.controller.Preflight(r.Context())
	report.Readiness = preflight.ReadinessOf(report)
	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) ListenAndServe(addr string) error {
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown корректно останавливает HTTP-сервер (graceful shutdown).
// RecordAdmissionFailure фиксирует run, который контроллер принял по 202,
// но который упал в фоне до создания durable state. Без этой записи run
// остался бы «призраком», невидимым в дашборде.
func (s *Server) RecordAdmissionFailure(runID, cause string) {
	now := time.Now().UTC()
	existing, err := s.store.GetPipelineRunByRunID(runID)
	if err != nil || existing == nil {
		run := &store.PipelineRun{RunID: runID, Feature: "unknown", Status: "queued", StartedAt: now}
		if createErr := s.store.CreatePipelineRun(run); createErr != nil {
			fmt.Fprintf(os.Stderr, "⚠ admission failure projection: %v\n", createErr)
			return
		}
	}
	changed, err := s.store.MarkRunAdmissionFailure(runID, cause, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ admission failure projection: %v\n", err)
		return
	}
	if !changed {
		// A scheduler recovery may already have correlated the durable queue job.
		// Its queue state is now authoritative and must not be overwritten by a
		// late foreground enqueue error.
		return
	}
	s.appendRunEvent(runID, "queue_failed", now, map[string]any{"status": "failed", "error": cause})
}

// RecordQueuedJob connects a scheduler row to the matching visible run.
func (s *Server) RecordQueuedJob(runID string, queueJobID int64) error {
	if err := s.store.MarkRunQueued(runID, queueJobID); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ queue projection: %v\n", err)
		return err
	}
	status := "queued"
	if run, err := s.store.GetPipelineRunByRunID(runID); err == nil && run != nil {
		status = run.Status
	}
	s.appendRunEvent(runID, "queue_updated", time.Now().UTC(), map[string]any{"queue_job_id": queueJobID, "status": status})
	return nil
}

// RecordQueueStatus projects worker-side preflight and execution outcomes that
// happen in a separate process after HTTP admission.
func (s *Server) RecordQueueStatus(queueJobID int64, status, cause string) {
	now := time.Now().UTC()
	runID, changed, err := s.store.UpdateQueueProjection(queueJobID, status, cause, now)
	if err != nil || runID == "" || !changed {
		return
	}
	s.appendRunEvent(runID, "queue_updated", now, map[string]any{"queue_job_id": queueJobID, "status": status, "error": cause})
}

func (s *Server) appendRunEvent(runID, eventType string, at time.Time, data any) {
	if err := s.appendRunEventRequired(runID, eventType, at, data); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ run event projection: %v\n", err)
	}
}

func (s *Server) appendRunEventRequired(runID, eventType string, at time.Time, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.store.AppendEventNext(&store.Event{RunID: runID, Type: eventType, Timestamp: at, DataJSON: string(encoded)})
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) Close() error {
	if s.cancelEvents != nil {
		s.cancelEvents()
		s.eventWorkers.Wait()
	}
	return s.store.Close()
}

func (s *Server) Store() *store.Store {
	return s.store
}

func (s *Server) Hub() *Hub {
	return s.hub
}

func (s *Server) replayEvents(cursor int64) ([]Event, error) {
	const pageSize = 500
	events := make([]Event, 0)
	for {
		stored, err := s.store.GetEventsAfter(cursor, pageSize)
		if err != nil {
			return nil, err
		}
		for _, item := range stored {
			event, err := wireEvent(item)
			if err != nil {
				return nil, err
			}
			event.Stream = s.streamID
			events = append(events, event)
			cursor = item.ID
		}
		if len(stored) < pageSize {
			return events, nil
		}
	}
}

func (s *Server) tailEvents(ctx context.Context, cursor int64) {
	defer s.eventWorkers.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				stored, queryErr := s.store.GetEventsAfter(cursor, 200)
				if queryErr != nil {
					fmt.Fprintf(os.Stderr, "web event stream: %v\n", queryErr)
					break
				}
				for _, item := range stored {
					event, convertErr := wireEvent(item)
					if convertErr != nil {
						fmt.Fprintf(os.Stderr, "web event stream: %v\n", convertErr)
						cursor = item.ID
						continue
					}
					event.Stream = s.streamID
					if !s.hub.BroadcastEventContext(ctx, event) {
						return
					}
					cursor = item.ID
				}
				if len(stored) < 200 {
					break
				}
			}
		}
	}
}

func (s *Server) handleGetPipelines(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pagination(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runs, err := s.store.GetPipelineRunsPage(limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if count, countErr := s.store.CountPipelineRuns(); countErr == nil {
		w.Header().Set("X-Total-Count", strconv.Itoa(count))
	}

	w.Header().Set("Content-Type", "application/json")
	// заголовки и статус уже отправлены: ошибку кодирования клиенту не передать, она означает оборванное соединение.
	_ = json.NewEncoder(w).Encode(runs)
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	run, err := s.store.GetPipelineRunByID(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	stages, err := s.store.GetStagesByPipelineRunID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"run":    run,
		"stages": stages,
	}
	if run.RunID != "" {
		response["delivery"] = s.deliveryProjection(run, stages, s.targetDir)
	}
	if run.RunID != "" {
		if stateStore, stateErr := lifecycle.NewStore(s.targetDir); stateErr == nil {
			if state, loadErr := stateStore.Load(run.RunID); loadErr == nil {
				response["next_stage"] = state.NextStage
			}
		}
		owners, ownerErr := s.store.GetStageOwners(run.RunID)
		if ownerErr != nil {
			http.Error(w, "stage owners unavailable", http.StatusInternalServerError)
			return
		}
		response["stage_owners"] = owners
	}
	if s.controller != nil && run.RunID != "" {
		approvals, approvalErr := s.controller.Approvals(run.RunID)
		if approvalErr != nil {
			http.Error(w, approvalErr.Error(), http.StatusInternalServerError)
			return
		}
		response["approvals"] = approvals
	}

	w.Header().Set("Content-Type", "application/json")
	// заголовки и статус уже отправлены: ошибку кодирования клиенту не передать, она означает оборванное соединение.
	_ = json.NewEncoder(w).Encode(response)
}

type webDeliveryProjection struct {
	Status string                   `json:"status"`
	Record *delivery.TerminalRecord `json:"record,omitempty"`
	Error  string                   `json:"error,omitempty"`
}

func (s *Server) deliveryProjection(run *store.PipelineRun, stages []store.Stage, target string) webDeliveryProjection {
	projection := webDeliveryProjection{Status: "not_requested"}
	for _, stage := range stages {
		if stage.DeliveryJSON == "" {
			continue
		}
		var marker struct {
			PlanHash string `json:"plan_hash"`
		}
		if json.Unmarshal([]byte(stage.DeliveryJSON), &marker) == nil && marker.PlanHash != "" {
			projection.Status = "pending"
			break
		}
	}
	record, found, err := delivery.ReadControllerDeliveryReceipt(target, run.RunID)
	if err != nil {
		projection.Status = "unavailable"
		return projection
	}
	if found {
		projection.Status = "recorded"
		projection.Record = record
		return projection
	}
	event, eventErr := s.store.LatestRunEvent(run.RunID, "delivery_retry_failed")
	if eventErr != nil {
		projection.Status = "unavailable"
		return projection
	}
	if event != nil {
		var data struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(event.DataJSON), &data) == nil && data.Error != "" {
			projection.Status = "failed"
			projection.Error = data.Error
		}
	}
	return projection
}

type artifactInfo struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // относительный к artifactRoot — используется в /api/artifacts/{path}
	RunID   string    `json:"run_id,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// handleGetArtifacts возвращает артефакты фичи запуска (walk по ФС).
func (s *Server) handleGetArtifacts(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	run, err := s.store.GetPipelineRunByID(id)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	artifacts := make([]artifactInfo, 0)
	if run.RunID != "" && filepath.Base(run.RunID) == run.RunID {
		runDir := filepath.Join(s.runRoot, run.RunID)
		for _, relativeRoot := range []string{"attempts", "reports", "brief"} {
			found, walkErr := walkArtifacts(runDir, filepath.Join(runDir, relativeRoot), run.RunID)
			if walkErr != nil {
				http.Error(w, "immutable evidence unavailable: "+walkErr.Error(), http.StatusInternalServerError)
				return
			}
			for _, artifact := range found {
				if allowedRunArtifactPath(artifact.Path) {
					artifacts = append(artifacts, artifact)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(artifacts)
		return
	}
	featureDir := filepath.Join(s.artifactRoot, run.Feature)
	artifacts, err = walkArtifacts(s.artifactRoot, featureDir, "")
	if err != nil {
		http.Error(w, "artifact storage unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// заголовки и статус уже отправлены: ошибку кодирования клиенту не передать, она означает оборванное соединение.
	_ = json.NewEncoder(w).Encode(artifacts)
}

// handleGetArtifact отдаёт содержимое артефакта строго внутри artifactRoot.
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	rel := chi.URLParam(r, "*")
	s.serveArtifact(w, r, s.artifactRoot, rel)
}

func (s *Server) handleGetRunArtifact(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if runID == "" || filepath.Base(runID) != runID {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	relative := chi.URLParam(r, "*")
	if !allowedRunArtifactPath(relative) {
		http.Error(w, "artifact path not exposed", http.StatusForbidden)
		return
	}
	s.serveArtifact(w, r, filepath.Join(s.runRoot, runID), relative)
}

type logTail struct {
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
	Offset    int64  `json:"offset"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

func (s *Server) handleGetRunLog(w http.ResponseWriter, r *http.Request) {
	runID, attemptID := chi.URLParam(r, "runID"), chi.URLParam(r, "attemptID")
	if !safeIdentity(runID) || !safeIdentity(attemptID) {
		http.Error(w, "invalid run or attempt id", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	logPath := filepath.Join(s.runRoot, runID, "logs", attemptID+".log")
	file, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(logTail{RunID: runID, AttemptID: attemptID})
			return
		}
		http.Error(w, "log unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }() // файл открыт на чтение: ошибка Close не меняет уже прочитанные данные.
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "log unavailable", http.StatusInternalServerError)
		return
	}
	offset := info.Size() - maxLogTailSize
	if offset < 0 {
		offset = 0
	}
	content := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(content, offset); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "log unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(logTail{
		RunID: runID, AttemptID: attemptID, Offset: offset,
		Truncated: offset > 0, Content: string(content),
	})
}

func (s *Server) handleGetRunWorkflow(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if !safeIdentity(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetPipelineRunByRunID(runID); err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	workflowPath := filepath.Join(s.runRoot, runID, "workflow.json")
	pathInfo, err := os.Lstat(workflowPath)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		http.Error(w, "workflow unavailable", http.StatusForbidden)
		return
	}
	file, err := os.Open(workflowPath)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}
	defer func() { _ = file.Close() }() // файл открыт на чтение: ошибка Close не меняет уже прочитанные данные.
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtifactSize {
		http.Error(w, "workflow unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func safeIdentity(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\`)
}

func allowedRunArtifactPath(relative string) bool {
	relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if strings.HasPrefix(relative, "reports/") {
		return true
	}
	if strings.HasPrefix(relative, "brief/") && strings.HasSuffix(relative, ".md") &&
		!strings.Contains(strings.TrimPrefix(relative, "brief/"), "/") {
		return true
	}
	parts := strings.Split(relative, "/")
	return len(parts) >= 4 && parts[0] == "attempts" && parts[1] != "" &&
		(parts[2] == "artifacts" || parts[2] == "inputs")
}

func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request, root, rel string) {
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	abs, err := resolveArtifactPath(root, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }() // файл открыт на чтение: ошибка Close не меняет уже прочитанные данные.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if info.Size() > maxArtifactSize {
		http.Error(w, "artifact too large", http.StatusRequestEntityTooLarge)
		return
	}

	if strings.HasSuffix(abs, ".md") {
		w.Header().Set("Content-Type", "text/markdown")
	} else {
		w.Header().Set("Content-Type", "text/plain")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func walkArtifacts(relativeRoot, start, runID string) ([]artifactInfo, error) {
	artifacts := make([]artifactInfo, 0)
	err := filepath.WalkDir(start, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && filePath == start {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact tree contains symbolic link")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact tree contains special file")
		}
		relative, err := filepath.Rel(relativeRoot, filePath)
		if err != nil {
			return nil
		}
		artifacts = append(artifacts, artifactInfo{
			Name: entry.Name(), Path: filepath.ToSlash(relative), RunID: runID,
			Size: info.Size(), ModTime: info.ModTime(),
		})
		return nil
	})
	return artifacts, err
}

func pagination(request *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, errors.New("limit must be between 1 and 100")
		}
	}
	if raw := request.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be non-negative")
		}
	}
	return limit, offset, nil
}

func resolveArtifactPath(root, rel string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return resolved, nil
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ServeWs(s.hub, w, r)
}

// handleFrontend обслуживает SPA-fallback. Сюда chi приводит любой GET, не
// совпавший с зарегистрированным роутом, — включая пути под /api/, которые
// раньше получали HTML страницы дашборда со статусом 200 вместо ошибки.
func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if requestIsAPI(r) {
		s.respondFallback(w, r)
		return
	}
	s.frontend.ServeHTTP(w, r)
}

// isAPIPath отделяет API от маршрутов клиентского роутера. Корень `/api` без
// слэша включён намеренно: это самый вероятный пробный URL автоматизированного
// клиента, и он тоже не должен получать HTML.
func isAPIPath(urlPath string) bool {
	return urlPath == "/api" || strings.HasPrefix(urlPath, "/api/")
}

// requestIsAPI считает запрос API-шным, если под /api/ попадает любая из форм
// пути — сырая или декодированная. Формы расходятся на percent-encoding, и
// ошибаться безопаснее в сторону JSON: отдать машиночитаемую ошибку там, где
// можно было отдать SPA, дешевле обратного.
func requestIsAPI(r *http.Request) bool {
	return isAPIPath(routingPath(r)) || isAPIPath(r.URL.Path)
}

// routingPath повторяет выбор пути, который делает chi.Mux.routeHTTP
// (mux.go:450-461): RoutePath из routing-контекста, иначе RawPath, иначе
// декодированный Path. Искать роуты по другому пути, чем маршрутизировал chi,
// значит отвечать про чужой ресурс: на `/api/run%73` декодированная форма
// совпадает с `/api/runs`, а сырая — ни с чем.
func routingPath(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}
	if r.URL.Path == "" {
		return "/"
	}
	return r.URL.Path
}

// spaCatchAllPattern — шаблон, под которым зарегистрирован SPA-fallback.
const spaCatchAllPattern = "/*"

// fallbackProbeMethods перебираются при построении Allow. Порядок
// алфавитный, чтобы заголовок был детерминированным; CONNECT и TRACE
// опущены — ни один роут под ними не регистрируется.
var fallbackProbeMethods = []string{
	http.MethodDelete, http.MethodGet, http.MethodHead,
	http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
}

// answeringMethods возвращает методы, которыми путь отвечает содержательно.
// Mux.Find — публичный поиск по дереву роутов, возвращающий шаблон совпавшего
// роута и не выполняющий хендлер.
func (s *Server) answeringMethods(routePath string, apiPath bool) []string {
	var methods []string
	for _, method := range fallbackProbeMethods {
		pattern := s.router.Find(chi.NewRouteContext(), method, routePath)
		if pattern == "" {
			continue
		}
		// Совпадение с SPA-катч-оллом на API-пути методом не считается:
		// handleFrontend отвечает там ошибкой, а не содержимым. Иначе Allow
		// обещал бы GET, а GET по тому же пути отдавал бы 404 — ровно та
		// самопротиворечивая пара, которой заголовок Allow быть не должен.
		if apiPath && pattern == spaCatchAllPattern {
			continue
		}
		methods = append(methods, method)
	}
	return methods
}

// respondFallback отвечает на запрос, не дошедший до роута. Различие между
// «нет такого пути» и «нет такого метода» здесь восстанавливается явно, а не
// берётся из ветки chi, поэтому ответ одинаков при любой конфигурации: путь,
// не отвечающий ни на один метод, получает 404, а промах по методу — 405 с
// Allow из методов, которые этот путь действительно обслуживает.
func (s *Server) respondFallback(w http.ResponseWriter, r *http.Request) {
	apiPath := requestIsAPI(r)
	// В теле ответа путь называется в той же форме, в какой его прислал
	// клиент: декодированный r.URL.Path сообщал бы про ресурс, которого тот
	// не запрашивал.
	requested := r.URL.EscapedPath()
	if methods := s.answeringMethods(routingPath(r), apiPath); len(methods) > 0 {
		for _, method := range methods {
			w.Header().Add("Allow", method)
		}
		if !apiPath {
			// Вне /api/ тело не нужно: так же отвечал дефолтный хендлер chi.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSONResponse(w, http.StatusMethodNotAllowed, map[string]any{
			"error":   "method_not_allowed",
			"detail":  fmt.Sprintf("%s is not supported for %s", r.Method, requested),
			"allowed": methods,
		})
		return
	}
	if !apiPath {
		http.NotFound(w, r)
		return
	}
	writeJSONResponse(w, http.StatusNotFound, map[string]string{
		"error":  "not_found",
		"detail": fmt.Sprintf("no API route matches %s %s", r.Method, requested),
	})
}

func spaHandler(distDir string) http.Handler {
	return spaFSHandler(os.DirFS(distDir))
}

func frontendHandler(distDir string) (http.Handler, error) {
	info, err := os.Stat(distDir)
	if err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("frontend dist path is not a directory: %s", distDir)
		}
		return spaHandler(distDir), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect frontend dist: %w", err)
	}

	embedded, err := fs.Sub(agentdata.Frontend, "web/dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded frontend: %w", err)
	}
	return spaFSHandler(embedded), nil
}

func spaFSHandler(frontend fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(frontend))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(frontend, name); err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					http.Error(w, "frontend unavailable", http.StatusInternalServerError)
					return
				}
				request := r.Clone(r.Context())
				request.URL.Path = "/"
				fileServer.ServeHTTP(w, request)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
