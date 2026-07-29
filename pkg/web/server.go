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
	Resume(runID string) error
	Cancel(runID string) error
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
	Approvals(runID string) ([]approval.PendingApproval, error)
	Preflight(context.Context) preflight.Report
}

type ServerOption func(*Server)

func WithRunController(controller RunController) ServerOption {
	return func(server *Server) { server.controller = controller }
}

type IdentityVerifier interface {
	Verify(token string) (cloudidentity.Principal, error)
}

func WithAuthenticator(verifier IdentityVerifier) ServerOption {
	return func(server *Server) { server.authenticator = verifier }
}

type browserSession struct {
	CSRFToken string
	Principal cloudidentity.Principal
	ExpiresAt time.Time
}

type Server struct {
	store         *store.Store
	hub           *Hub
	router        *chi.Mux
	frontend      http.Handler
	artifactRoot  string // абсолютный корень артефактов; всё вне него не отдаётся
	runRoot       string // immutable .ai-team/runs root
	httpServer    *http.Server
	cancelEvents  context.CancelFunc
	eventWorkers  sync.WaitGroup
	controller    RunController
	authenticator IdentityVerifier
	sessions      map[string]browserSession
	sessionMu     sync.Mutex
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
		s.Close()
		return nil, err
	}

	hub := NewHub()

	srv := &Server{
		store:        s,
		hub:          hub,
		artifactRoot: absRoot,
		runRoot:      filepath.Join(filepath.Dir(absRoot), "runs"),
		sessions:     make(map[string]browserSession),
	}
	for _, option := range options {
		option(srv)
	}
	hub.SetReplay(srv.replayEvents)
	go hub.Run()
	eventContext, cancelEvents := context.WithCancel(context.Background())
	srv.cancelEvents = cancelEvents
	eventCursor, err := s.LatestEventCursor()
	if err != nil {
		cancelEvents()
		s.Close()
		return nil, fmt.Errorf("initial event cursor: %w", err)
	}
	srv.eventWorkers.Add(1)
	go srv.tailEvents(eventContext, eventCursor)

	srv.router = chi.NewRouter()
	srv.router.Use(middleware.Recoverer)
	if srv.authenticator == nil {
		srv.router.Use(sameOriginMiddleware)
	} else {
		srv.router.Use(authenticatedOriginMiddleware)
	}

	srv.router.Get("/api/auth/config", srv.handleAuthConfig)
	srv.router.Get("/api/session", srv.handleSession)
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.readSecurity)
		router.Get("/api/pipelines", srv.handleGetPipelines)
		router.Get("/api/pipelines/{id}", srv.handleGetPipeline)
		router.Get("/api/pipelines/{id}/artifacts", srv.handleGetArtifacts)
		router.Get("/api/artifacts/*", srv.handleGetArtifact)
		router.Get("/api/runs/{runID}/artifacts/*", srv.handleGetRunArtifact)
		router.Get("/api/runs/{runID}/logs/{attemptID}", srv.handleGetRunLog)
		router.Get("/api/runs/{runID}/workflow", srv.handleGetRunWorkflow)
		router.Get("/api/preflight", srv.handlePreflight)
		router.Get("/api/auth/me", srv.handleCurrentIdentity)
	})
	srv.router.Group(func(router chi.Router) {
		router.Use(srv.writeSecurity)
		router.Post("/api/runs", srv.handleStartRun)
		router.Post("/api/runs/{runID}/resume", srv.handleResumeRun)
		router.Post("/api/runs/{runID}/cancel", srv.handleCancelRun)
		router.Post("/api/runs/{runID}/approvals/{approvalID}/decisions", srv.handleDecision)
	})
	srv.router.With(srv.readSecurity).Get("/ws", srv.handleWebSocket)

	if distDir != "" {
		srv.frontend, err = frontendHandler(distDir)
		if err != nil {
			s.Close()
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
	_ = json.NewEncoder(w).Encode(s.controller.Preflight(r.Context()))
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
	json.NewEncoder(w).Encode(runs)
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
		target := filepath.Dir(filepath.Dir(s.artifactRoot))
		if stateStore, stateErr := lifecycle.NewStore(target); stateErr == nil {
			if state, loadErr := stateStore.Load(run.RunID); loadErr == nil {
				response["next_stage"] = state.NextStage
			}
		}
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
	json.NewEncoder(w).Encode(response)
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
		for _, relativeRoot := range []string{"attempts", "reports"} {
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
	json.NewEncoder(w).Encode(artifacts)
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
	defer file.Close()
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
	defer file.Close()
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
	defer f.Close()
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

func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	s.frontend.ServeHTTP(w, r)
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
