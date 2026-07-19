package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := NewServer(":memory:", "")
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestGetPipelines_Empty(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var runs []interface{}
	if err := json.NewDecoder(w.Body).Decode(&runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected empty array, got %d items", len(runs))
	}
}

func TestGetPipelines_WithData(t *testing.T) {
	srv := newTestServer(t)

	srv.Store().CreatePipelineRun(&store.PipelineRun{
		Feature:   "test-feat",
		Status:    "running",
		StartedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var runs []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&runs)

	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0]["feature"] != "test-feat" {
		t.Errorf("expected feature 'test-feat', got %v", runs[0]["feature"])
	}
}

func TestGetPipelineByID(t *testing.T) {
	srv := newTestServer(t)

	run := &store.PipelineRun{Feature: "detail-test", Status: "completed", StartedAt: time.Now()}
	srv.Store().CreatePipelineRun(run)

	req := httptest.NewRequest("GET", "/api/pipelines/1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["run"] == nil {
		t.Error("expected 'run' in response")
	}
	if resp["stages"] == nil {
		t.Error("expected 'stages' in response")
	}
}

func TestGetPipelineByID_NotFound(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines/999", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetPipelineByID_InvalidID(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines/abc", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGetArtifacts(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines/1/artifacts", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestGetArtifact_DirectoryTraversal(t *testing.T) {
	srv := newTestServer(t)

	// Test various traversal patterns
	tests := []struct {
		name string
		path string
	}{
		{"dotdot", "foo/../../etc/passwd"},
		{"encoded", "foo%2F..%2F..%2Fetc%2Fpasswd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/artifacts/"+tt.path, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				t.Errorf("expected non-200 for traversal path %q, got 200", tt.path)
			}
		})
	}
}

func TestGetArtifact_NotFound(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/artifacts/nonexistent.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetArtifact_Markdown(t *testing.T) {
	srv := newTestServer(t)

	dir := t.TempDir()
	mdFile := filepath.Join(dir, "test.md")
	os.WriteFile(mdFile, []byte("# Hello"), 0644)

	req := httptest.NewRequest("GET", "/api/artifacts"+mdFile, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/markdown" {
		t.Errorf("expected text/markdown, got %s", ct)
	}
	if w.Body.String() != "# Hello" {
		t.Errorf("expected '# Hello', got %q", w.Body.String())
	}
}

func TestCORS(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("OPTIONS", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS Allow-Origin header")
	}
}

func TestSPAHandler(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('hi')"), 0644)

	handler := spaHandler(dir)

	t.Run("serves existing file", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/app.js", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("falls back to index.html for unknown routes", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/unknown/route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != "<html>SPA</html>" {
			t.Errorf("expected SPA fallback, got %q", w.Body.String())
		}
	})
}

func TestResolveArtifactPath(t *testing.T) {
	got := ResolveArtifactPath("my-feature", ".ai-team/artifacts")
	expected := ".ai-team/artifacts/my-feature"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
