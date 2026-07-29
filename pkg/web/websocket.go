package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Same-origin: пустой Origin (не-браузерные клиенты) разрешён; иначе
	// Origin должен резолвиться на loopback-хост — сравнение с r.Host не
	// защищает от DNS rebinding, т.к. оба заголовка одинаково отражают
	// домен атакующего при rebind (см. isLoopbackHostname).
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return isLoopbackHostname(u.Host) || strings.EqualFold(u.Host, r.Host)
	},
}

type Event struct {
	Version   int            `json:"version"`
	Cursor    int64          `json:"cursor"`
	RunID     string         `json:"run_id"`
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"type"`
	AttemptID string         `json:"attempt_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan Event
	register   chan registration
	unregister chan *Client
	replay     func(int64) ([]Event, error)
	mu         sync.RWMutex
}

type registration struct {
	client *Client
	cursor int64
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan Event, 256),
		register:   make(chan registration),
		unregister: make(chan *Client),
	}
}

func (h *Hub) SetReplay(replay func(int64) ([]Event, error)) {
	h.replay = replay
}

func (h *Hub) Run() {
	for {
		select {
		case request := <-h.register:
			if h.replay != nil {
				events, err := h.replay(request.cursor)
				if err != nil {
					log.Printf("websocket: replay failed: %v", err)
					close(request.client.send)
					continue
				}
				for _, event := range events {
					data, err := json.Marshal(event)
					if err != nil {
						log.Printf("websocket: failed to marshal replay event: %v", err)
						continue
					}
					request.client.send <- data
				}
			}
			h.mu.Lock()
			h.clients[request.client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

		case event := <-h.broadcast:
			message, err := json.Marshal(event)
			if err != nil {
				log.Printf("websocket: failed to marshal event: %v", err)
				continue
			}
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) BroadcastEvent(event Event) {
	// Durable tailer applies backpressure here instead of dropping a cursor in
	// the middle of an otherwise ordered live stream. SQLite remains the
	// replay source while the bounded queue drains.
	h.broadcast <- event
}

func (h *Hub) BroadcastEventContext(ctx context.Context, event Event) bool {
	select {
	case h.broadcast <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	cursor := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		cursor = parsed
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket: upgrade error: %v", err)
		return
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	go client.writePump()
	hub.register <- registration{client: client, cursor: cursor}
	go client.readPump()
}

func wireEvent(event store.Event) (Event, error) {
	data := make(map[string]any)
	if event.DataJSON != "" {
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			return Event{}, fmt.Errorf("event %d payload: %w", event.ID, err)
		}
	}
	return Event{
		Version: 1, Cursor: event.ID, RunID: event.RunID, Sequence: event.Sequence,
		Type: event.Type, AttemptID: event.AttemptID, Timestamp: event.Timestamp, Data: data,
	}, nil
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(1 << 20)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
