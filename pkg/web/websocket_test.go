package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
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

// wsReadBudget — потолок чтения одного сообщения из уже зарегистрированного
// клиента. Сообщение идёт через каналы внутри процесса, то есть измеряется
// микросекундами; бюджет отличает зависший writePump от загруженной машины, а
// не задаёт ожидаемое время.
const wsReadBudget = 10 * time.Second

// waitForClients ждёт, пока хаб придёт к ожидаемому числу зарегистрированных
// клиентов.
//
// Регистрация асинхронна: ServeWs завершает handshake раньше, чем hub.Run()
// вносит клиента в карту, а на завершение регистрации подписаться нечем.
// Фиксированный time.Sleep здесь был скрытым дедлайном — на загруженной машине
// 50 мс не хватает, и тест падал не из-за поведения хаба. Опрос выходит сразу
// по факту, а бюджет остаётся аварийным потолком.
func waitForClients(t *testing.T, hub *Hub, expected int) {
	t.Helper()
	const budget = 10 * time.Second
	deadline := time.Now().Add(budget)
	for {
		hub.mu.RLock()
		count := len(hub.clients)
		hub.mu.RUnlock()
		if count == expected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("хаб не пришёл к %d клиентам за %s: сейчас %d", expected, budget, count)
		}
		time.Sleep(5 * time.Millisecond)
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
	defer func() { _ = conn.Close() }()

	// Wait for registration
	waitForClients(t, hub, 1)

	// Broadcast an event
	event := Event{
		Version: 1, Cursor: 1, RunID: "run-1", Sequence: 2,
		Type: "attempt_started", Timestamp: time.Now(),
		Data: map[string]any{"agent": "analyst", "status": "running"},
	}
	hub.BroadcastEventContext(context.Background(), event)

	// Read the message
	if err := conn.SetReadDeadline(time.Now().Add(wsReadBudget)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}

	var received Event
	if err := json.Unmarshal(msg, &received); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if received.Type != "attempt_started" {
		t.Errorf("expected type 'attempt_started', got %q", received.Type)
	}
	if received.Cursor != 1 {
		t.Errorf("expected cursor 1, got %d", received.Cursor)
	}
	if received.Data["agent"] != "analyst" {
		t.Errorf("expected agent 'analyst', got %q", received.Data["agent"])
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
		defer func() { _ = conn.Close() }()
		conns = append(conns, conn)
	}

	waitForClients(t, hub, 3)

	// Broadcast
	hub.BroadcastEventContext(context.Background(), Event{Version: 1, Type: "test", Data: map[string]any{"agent": "broadcast-test"}})

	// All 3 should receive
	for i, conn := range conns {
		if err := conn.SetReadDeadline(time.Now().Add(wsReadBudget)); err != nil {
			t.Fatal(err)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client %d failed to read: %v", i, err)
		}
		var ev Event
		if err := json.Unmarshal(msg, &ev); err != nil {
			t.Fatalf("client %d: unmarshal: %v", i, err)
		}
		if ev.Data["agent"] != "broadcast-test" {
			t.Errorf("client %d: expected 'broadcast-test', got %q", i, ev.Data["agent"])
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

	waitForClients(t, hub, 1)

	// Close the connection — readPump should unregister
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClients(t, hub, 0)
}

func TestEvent_MarshalJSON(t *testing.T) {
	event := Event{
		Version: 1, Cursor: 42, RunID: "run-42", Sequence: 3,
		Type: "attempt_finished", Timestamp: time.Now(),
		Data: map[string]any{"agent": "coder", "status": "passed", "duration_ms": float64(1234)},
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
	if decoded.Cursor != event.Cursor {
		t.Errorf("cursor mismatch")
	}
	if decoded.Data["duration_ms"] != event.Data["duration_ms"] {
		t.Errorf("data mismatch")
	}
}

func TestEvent_OmitEmpty(t *testing.T) {
	event := Event{Version: 1, Type: "ping", Data: map[string]any{}}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	s := string(data)
	if strings.Contains(s, "attempt_id") {
		t.Error("attempt_id should be omitted when empty")
	}
}

func TestWebSocketReplaysAfterCursor(t *testing.T) {
	server, err := NewServer(filepath.Join(t.TempDir(), "web.db"), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	now := time.Now().UTC()
	first := &store.Event{RunID: "run-replay", Sequence: 1, Type: "run_started", Timestamp: now, DataJSON: `{"feature":"replay"}`}
	second := &store.Event{RunID: "run-replay", Sequence: 2, Type: "run_finished", Timestamp: now.Add(time.Second), DataJSON: `{"status":"completed"}`}
	if err := server.Store().AppendEvent(first); err != nil {
		t.Fatal(err)
	}
	if err := server.Store().AppendEvent(second); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(server.router)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws?cursor=" + strconv.FormatInt(first.ID, 10)
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v status=%v", err, responseStatus(response))
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetReadDeadline(time.Now().Add(wsReadBudget)); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Cursor != second.ID || event.Type != "run_finished" || event.Version != 1 {
		t.Fatalf("неожиданный replay event: %+v", event)
	}
}

func TestWebSocketBridgePublishesSQLiteEvent(t *testing.T) {
	server, err := NewServer(filepath.Join(t.TempDir(), "web.db"), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	httpServer := httptest.NewServer(server.router)
	defer httpServer.Close()
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v status=%v", err, responseStatus(response))
	}
	defer func() { _ = connection.Close() }()
	// Событие живой ленты рассылается только уже зарегистрированным клиентам:
	// успешный Dial этого ещё не означает, и запись до регистрации была бы
	// потеряна безвозвратно — ReadJSON тогда ждал бы весь свой бюджет впустую.
	waitForClients(t, server.hub, 1)

	stored := &store.Event{
		RunID: "run-live", Sequence: 1, Type: "run_started",
		Timestamp: time.Now().UTC(), DataJSON: `{"feature":"live"}`,
	}
	if err := server.Store().AppendEvent(stored); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(wsReadBudget)); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Cursor != stored.ID || event.RunID != "run-live" {
		t.Fatalf("неожиданный live event: %+v", event)
	}
}

func TestWireEventRejectsMalformedPayload(t *testing.T) {
	_, err := wireEvent(store.Event{ID: 7, RunID: "run", DataJSON: `[`})
	if err == nil || !strings.Contains(err.Error(), "event 7 payload") {
		t.Fatalf("ожидалась контекстная ошибка payload, получено %v", err)
	}
}

func responseStatus(response *http.Response) string {
	if response == nil {
		return "<nil>"
	}
	return response.Status
}
