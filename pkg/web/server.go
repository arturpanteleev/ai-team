package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "modernc.org/sqlite"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type Server struct {
	store    *store.Store
	hub      *Hub
	router   *chi.Mux
	frontend http.Handler
}

func NewServer(dbPath string, distDir string) (*Server, error) {
	s, err := store.New(dbPath)
	if err != nil {
		return nil, err
	}

	hub := NewHub()
	go hub.Run()

	srv := &Server{
		store: s,
		hub:   hub,
	}

	srv.router = chi.NewRouter()
	srv.router.Use(middleware.Logger)
	srv.router.Use(middleware.Recoverer)
	srv.router.Use(corsMiddleware)

	srv.router.Get("/api/pipelines", srv.handleGetPipelines)
	srv.router.Get("/api/pipelines/{id}", srv.handleGetPipeline)
	srv.router.Get("/api/pipelines/{id}/artifacts", srv.handleGetArtifacts)
	srv.router.Get("/api/artifacts/*", srv.handleGetArtifact)
	srv.router.Get("/ws", srv.handleWebSocket)

	if distDir != "" {
		srv.frontend = spaHandler(distDir)
		srv.router.Get("/*", srv.handleFrontend)
	}

	return srv, nil
}

func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.router)
}

func (s *Server) Close() error {
	return s.store.Close()
}

func (s *Server) Store() *store.Store {
	return s.store
}

func (s *Server) Hub() *Hub {
	return s.hub
}

func (s *Server) handleGetPipelines(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.GetPipelineRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleGetArtifacts(w http.ResponseWriter, r *http.Request) {
	// For now, return empty array - artifacts are on filesystem
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode([]interface{}{})
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Chi * strips leading slash — restore it for absolute paths
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Security: prevent directory traversal
	if strings.Contains(path, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if strings.HasSuffix(path, ".md") {
		w.Header().Set("Content-Type", "text/markdown")
	} else {
		w.Header().Set("Content-Type", "text/plain")
	}
	w.Write(data)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ServeWs(s.hub, w, r)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func ResolveArtifactPath(feature, artifactRoot string) string {
	return filepath.Join(artifactRoot, feature)
}

func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	s.frontend.ServeHTTP(w, r)
}

func spaHandler(distDir string) http.Handler {
	fs := http.FileServer(http.Dir(distDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(distDir, r.URL.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}
