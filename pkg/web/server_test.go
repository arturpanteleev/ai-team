package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	artifactRoot := t.TempDir()
	srv, err := NewServer(":memory:", "", artifactRoot)
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, artifactRoot
}

func TestGetPipelines_Empty(t *testing.T) {
	srv, _ := newTestServer(t)

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
	srv, _ := newTestServer(t)

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

func TestGetPipelinesPagination(t *testing.T) {
	srv, _ := newTestServer(t)
	for index := 0; index < 3; index++ {
		if err := srv.Store().CreatePipelineRun(&store.PipelineRun{RunID: fmt.Sprintf("run-%d", index), Feature: "f", Status: "completed", StartedAt: time.Now().Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/api/pipelines?limit=1&offset=1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var runs []store.PipelineRun
	_ = json.NewDecoder(w.Body).Decode(&runs)
	if w.Code != http.StatusOK || len(runs) != 1 || w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("pagination response: code=%d total=%s runs=%+v", w.Code, w.Header().Get("X-Total-Count"), runs)
	}
	bad := httptest.NewRequest("GET", "/api/pipelines?limit=1000", nil)
	badWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(badWriter, bad)
	if badWriter.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination must be 400, got %d", badWriter.Code)
	}
}

func TestGetPipelineByID(t *testing.T) {
	srv, _ := newTestServer(t)

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
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines/999", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetPipelineByID_InvalidID(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines/abc", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGetArtifacts_ListsFeatureFiles(t *testing.T) {
	srv, root := newTestServer(t)

	run := &store.PipelineRun{Feature: "feat-x", Status: "completed", StartedAt: time.Now()}
	srv.Store().CreatePipelineRun(run)

	featureDir := filepath.Join(root, "feat-x")
	os.MkdirAll(featureDir, 0755)
	os.WriteFile(filepath.Join(featureDir, "proposal.md"), []byte("# P"), 0644)

	req := httptest.NewRequest("GET", "/api/pipelines/1/artifacts", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var artifacts []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&artifacts)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0]["path"] != "feat-x/proposal.md" {
		t.Errorf("path = %v", artifacts[0]["path"])
	}
}

func TestGetArtifactsUnknownRunReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/pipelines/999/artifacts", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestGetArtifactsUsesImmutableRunEvidence(t *testing.T) {
	srv, root := newTestServer(t)
	runID := "20260720T000000.000000000Z-0123456789abcdef"
	run := &store.PipelineRun{RunID: runID, Feature: "feat", Status: "completed", StartedAt: time.Now()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(root), "runs", runID)
	evidenceFile := filepath.Join(runDir, "attempts", "001-analyst", "artifacts", "proposal", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(evidenceFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidenceFile, []byte("immutable"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "feat"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "feat", "proposal.md"), []byte("live-mutated"), 0644)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/pipelines/%d/artifacts", run.ID), nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var artifacts []artifactInfo
	_ = json.NewDecoder(w.Body).Decode(&artifacts)
	if len(artifacts) != 1 || artifacts[0].RunID != runID {
		t.Fatalf("immutable listing: %+v", artifacts)
	}
	raw := httptest.NewRequest("GET", "/api/runs/"+runID+"/artifacts/"+artifacts[0].Path, nil)
	rawWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(rawWriter, raw)
	if rawWriter.Code != http.StatusOK || rawWriter.Body.String() != "immutable" {
		t.Fatalf("immutable artifact: code=%d body=%q", rawWriter.Code, rawWriter.Body.String())
	}
}

// Артефакты отдаются ТОЛЬКО внутри artifactRoot: абсолютные пути и traversal
// за пределы корня недоступны (регрессия против arbitrary file read).
func TestGetArtifact_ConfinedToRoot(t *testing.T) {
	srv, root := newTestServer(t)

	os.MkdirAll(filepath.Join(root, "feat"), 0755)
	os.WriteFile(filepath.Join(root, "feat", "review.md"), []byte("# Review"), 0644)

	// Файл вне корня
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0644)
	t.Cleanup(func() { os.Remove(outside) })

	t.Run("valid relative path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/artifacts/feat/review.md", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/markdown" {
			t.Errorf("expected text/markdown, got %s", ct)
		}
		if w.Body.String() != "# Review" {
			t.Errorf("body = %q", w.Body.String())
		}
	})

	for _, path := range []string{
		"/api/artifacts/../secret.txt",
		"/api/artifacts/feat/../../secret.txt",
		"/api/artifacts/" + outside, // абсолютный путь
		"/api/artifacts/etc/passwd",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				t.Errorf("путь %q не должен отдаваться (код %d)", path, w.Code)
			}
		})
	}
}

func TestGetArtifact_RejectsSymlinkOutsideRoot(t *testing.T) {
	srv, root := newTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, []byte("secret"), 0644)
	os.MkdirAll(filepath.Join(root, "feat"), 0755)
	link := filepath.Join(root, "feat", "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/artifacts/feat/link.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("symlink outside artifact root не должен читаться: %s", w.Body.String())
	}
}

func TestGetArtifact_RejectsOversizedFile(t *testing.T) {
	srv, root := newTestServer(t)
	path := filepath.Join(root, "large.md")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxArtifactSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	req := httptest.NewRequest("GET", "/api/artifacts/large.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestGetArtifact_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/artifacts/nonexistent.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestNoCORSWildcard(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/pipelines", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS wildcard не должен выставляться")
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

func TestNewServerFallsBackToEmbeddedFrontend(t *testing.T) {
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", filepath.Join(t.TempDir(), "missing-dist"), artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	request := httptest.NewRequest(http.MethodGet, "/pipelines/123", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected embedded SPA response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("response does not contain embedded frontend marker: %q", response.Body.String())
	}
}
