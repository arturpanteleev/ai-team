package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const secretPath = "/hooks/T000/SECRET-TOKEN"

func sampleNotification() Notification {
	return Notification{
		EventID: 42, RunID: "run-42", TaskID: 9, Type: "stage_stalled", Stage: "qa", Function: "qa",
		Severity: "warning", Timestamp: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
		TaskURL: "https://ai-team.example/tasks/run-42", Data: map[string]any{"stalled_for": "24h"},
	}
}

func assertNoLeak(t *testing.T, err error, server *httptest.Server) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	text := err.Error()
	if strings.Contains(text, "SECRET-TOKEN") || strings.Contains(text, "/hooks/") ||
		(server != nil && strings.Contains(text, strings.TrimPrefix(server.URL, "https://"))) {
		t.Fatalf("error leaks destination: %q", text)
	}
}

func TestWebhookPostsEventJSONWithIdempotencyKey(t *testing.T) {
	type received struct {
		path, contentType, idempotency, eventID string
		payload                                 map[string]any
	}
	requests := make(chan received, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		requests <- received{r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key"), r.Header.Get("X-AI-Team-Event-ID"), payload}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	channel := NewWebhookChannel("ops", server.URL+secretPath, server.Client())
	if channel.Name() != "ops" {
		t.Fatalf("name=%q", channel.Name())
	}
	if err := channel.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	if got.path != secretPath || got.contentType != "application/json" || got.idempotency != "ai-team-event-42" || got.eventID != "42" {
		t.Fatalf("request=%+v", got)
	}
	if got.payload["type"] != "stage_stalled" || got.payload["function"] != "qa" || got.payload["event_id"] != float64(42) ||
		got.payload["task_url"] != "https://ai-team.example/tasks/run-42" || !strings.Contains(got.payload["summary"].(string), "task #9") {
		t.Fatalf("payload=%v", got.payload)
	}
}

func TestSlackPostsTextToIncomingWebhook(t *testing.T) {
	bodies := make(chan map[string]string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies <- body
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	if err := NewSlackChannel("team", server.URL+secretPath, server.Client()).WithTimeout(time.Second).Send(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	body := <-bodies
	if !strings.HasPrefix(body["text"], "[ai-team] stage_stalled · task #9 · stage qa · warning\n") || !strings.HasSuffix(body["text"], "/tasks/run-42") {
		t.Fatalf("slack text=%q", body["text"])
	}
}

func TestWebhookRejectsRedirectToForeignHost(t *testing.T) {
	var foreignHits atomic.Int32
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	err := NewWebhookChannel("ops", origin.URL+secretPath, origin.Client()).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, origin)
	if !strings.Contains(err.Error(), "redirect to a different host was rejected") {
		t.Fatalf("error=%v", err)
	}
	if foreignHits.Load() != 0 {
		t.Fatal("payload reached the foreign redirect target")
	}
}

func TestWebhookFollowsSameHostRedirect(t *testing.T) {
	var final atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == secretPath {
			http.Redirect(w, r, "/moved", http.StatusPermanentRedirect)
			return
		}
		final.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := NewWebhookChannel("ops", server.URL+secretPath, server.Client()).Send(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	if final.Load() != 1 {
		t.Fatal("same-host redirect was not followed")
	}
}

func TestWebhookFailuresAreSanitized(t *testing.T) {
	failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream says "+secretPath, http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	err := NewWebhookChannel("ops", failing.URL+secretPath, failing.Client()).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, failing)
	if err.Error() != "webhook: HTTP 503" {
		t.Fatalf("error=%q", err)
	}

	unreachable := httptest.NewTLSServer(http.NotFoundHandler())
	unreachableURL, client := unreachable.URL, unreachable.Client()
	unreachable.Close()
	err = NewWebhookChannel("ops", unreachableURL+secretPath, client).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, unreachable)
	if !strings.HasPrefix(err.Error(), "webhook: connection failed") {
		t.Fatalf("unreachable error=%q", err)
	}

	untrusted := httptest.NewTLSServer(http.NotFoundHandler())
	defer untrusted.Close()
	err = NewSlackChannel("team", untrusted.URL+secretPath, nil).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, untrusted)
	if err.Error() != "slack: TLS verification failed" {
		t.Fatalf("untrusted certificate error=%q", err)
	}
}

func TestWebhookTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	err := NewWebhookChannel("ops", server.URL+secretPath, server.Client()).WithTimeout(50*time.Millisecond).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, server)
	if err.Error() != "webhook: request timed out" {
		t.Fatalf("error=%q", err)
	}
}

func TestWebhookRequiresHTTPSDestination(t *testing.T) {
	for _, raw := range []string{"", "http://hooks.example" + secretPath, "https://user:pass@hooks.example" + secretPath, "hooks.example" + secretPath, "https://"} {
		err := NewWebhookChannel("ops", raw, nil).Send(context.Background(), sampleNotification())
		assertNoLeak(t, err, nil)
		if strings.Contains(err.Error(), "pass") {
			t.Fatalf("error leaks credentials: %q", err)
		}
	}
	if err := NewSlackChannel("team", "", nil).Send(context.Background(), sampleNotification()); err == nil || err.Error() != "slack: destination URL is not configured" {
		t.Fatalf("missing env value error=%v", err)
	}
}

func TestWebhookRejectsRedirectToSameHostOnAnotherPort(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	_, otherPort, err := net.SplitHostPort(other.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		http.Redirect(w, r, "https://"+net.JoinHostPort(host, otherPort)+"/collect", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	err = NewWebhookChannel("ops", origin.URL+secretPath, origin.Client()).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, origin)
	if !strings.Contains(err.Error(), "redirect to a different host was rejected") {
		t.Fatalf("error=%v", err)
	}
	if otherHits.Load() != 0 {
		t.Fatal("payload reached another port of the same host")
	}
}

func TestWebhookRejectsRedirectFromHTTPSToHTTP(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Same host and port, plain http: following it would send the
		// payload unencrypted.
		http.Redirect(w, r, "http://"+r.Host+"/collect", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	err := NewWebhookChannel("ops", origin.URL+secretPath, origin.Client()).Send(context.Background(), sampleNotification())
	assertNoLeak(t, err, origin)
	if !strings.Contains(err.Error(), "redirect to a different host was rejected") {
		t.Fatalf("https→http redirect must be rejected by policy, error=%v", err)
	}
}

func TestSameHTTPSOriginNormalizesDefaultPortAndCase(t *testing.T) {
	parse := func(raw string) *url.URL {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	destination := parse("https://hooks.example/path")
	for raw, want := range map[string]bool{
		"https://hooks.example/other":     true,
		"https://HOOKS.example:443/other": true,
		"https://hooks.example:8443/x":    false,
		"http://hooks.example/x":          false,
		"http://hooks.example:443/x":      false,
		"https://evil.example/x":          false,
	} {
		if got := sameHTTPSOrigin(parse(raw), destination); got != want {
			t.Errorf("sameHTTPSOrigin(%q)=%v, want %v", raw, got, want)
		}
	}
	if !sameHTTPSOrigin(parse("https://hooks.example/x"), parse("https://hooks.example:443/")) {
		t.Error("explicit :443 destination must match an omitted port")
	}
}
