package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestUpgraderCheckOriginRejectsRebindStyleOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Host = "127.0.0.1:8080" // the connection genuinely landed on loopback
	req.Header.Set("Origin", "http://evil.example:8080")
	if upgrader.CheckOrigin(req) {
		t.Fatal("Origin naming a non-loopback host must be rejected even when Host is loopback")
	}
}

func TestUpgraderCheckOriginAllowsLoopbackOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	if !upgrader.CheckOrigin(req) {
		t.Fatal("loopback Origin must be allowed")
	}
}

func TestUpgraderCheckOriginAllowsMissingOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Host = "127.0.0.1:8080"
	if !upgrader.CheckOrigin(req) {
		t.Fatal("non-browser clients without an Origin header must be allowed")
	}
}

func TestNewHub(t *testing.T) {
	hub := NewHub()
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}
	if hub.clients == nil {
		t.Error("expected clients map")
	}
	if hub.broadcast == nil {
		t.Error("expected broadcast channel")
	}
}

func TestHub_BroadcastEvent(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Connect a client
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	// Broadcast an event
	event := Event{
		Type:       "stage_started",
		PipelineID: 1,
		Agent:      "analyst",
		Status:     "running",
	}
	hub.BroadcastEvent(event)

	// Read the message
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}

	var received Event
	if err := json.Unmarshal(msg, &received); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if received.Type != "stage_started" {
		t.Errorf("expected type 'stage_started', got %q", received.Type)
	}
	if received.PipelineID != 1 {
		t.Errorf("expected pipeline_id 1, got %d", received.PipelineID)
	}
	if received.Agent != "analyst" {
		t.Errorf("expected agent 'analyst', got %q", received.Agent)
	}
}

func TestHub_MultipleClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Connect 3 clients
	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("failed to dial client %d: %v", i, err)
		}
		defer conn.Close()
		conns = append(conns, conn)
	}

	time.Sleep(100 * time.Millisecond)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 3 {
		t.Errorf("expected 3 clients, got %d", clientCount)
	}

	// Broadcast
	hub.BroadcastEvent(Event{Type: "test", Agent: "broadcast-test"})

	// All 3 should receive
	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client %d failed to read: %v", i, err)
		}
		var ev Event
		json.Unmarshal(msg, &ev)
		if ev.Agent != "broadcast-test" {
			t.Errorf("client %d: expected 'broadcast-test', got %q", i, ev.Agent)
		}
	}
}

func TestHub_Unregister(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	hub.mu.RLock()
	count := len(hub.clients)
	hub.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected 1 client, got %d", count)
	}

	// Close the connection — readPump should unregister
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	hub.mu.RLock()
	count = len(hub.clients)
	hub.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 clients after close, got %d", count)
	}
}

func TestEvent_MarshalJSON(t *testing.T) {
	event := Event{
		Type:       "stage_completed",
		PipelineID: 42,
		Agent:      "coder",
		Status:     "passed",
		DurationMs: 1234,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != event.Type {
		t.Errorf("type mismatch: %q vs %q", decoded.Type, event.Type)
	}
	if decoded.PipelineID != event.PipelineID {
		t.Errorf("pipeline_id mismatch")
	}
	if decoded.DurationMs != event.DurationMs {
		t.Errorf("duration_ms mismatch")
	}
}

func TestEvent_OmitEmpty(t *testing.T) {
	event := Event{Type: "ping"}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	s := string(data)
	if strings.Contains(s, "pipeline_id") {
		t.Error("pipeline_id should be omitted when zero")
	}
	if strings.Contains(s, "agent") {
		t.Error("agent should be omitted when empty")
	}
}
