package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
)

func TestVersionEndpointReportsBackendBuild(t *testing.T) {
	srv, _ := newPickupServer(t, ":memory:", t.TempDir(), autopickup.Policy{}, &fixedLedger{}, time.Now().UTC())
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/version", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("version: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Backend buildRevision `json:"backend"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("version must not be cached")
	}
}
