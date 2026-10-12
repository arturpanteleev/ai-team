package notifier

import (
	"context"
	"sync"
	"time"
)

// Event is the structured task event sent to live observers after the event
// has been durably projected to the event store.
type Event struct {
	RunID     string
	Sequence  int64
	Type      string
	Stage     string
	Actor     string
	Executor  string
	Severity  string
	AttemptID string
	Timestamp time.Time
	Data      map[string]any
}

// EventSubscriber is implemented by event sources that support live
// subscriptions. Events remain durable in SQLite, so subscribers can resume
// from a cursor after a disconnect.
type EventSubscriber interface {
	Subscribe(context.Context, int) <-chan Event
}

// EventPublisher is implemented by event sources that accept projected events.
type EventPublisher interface {
	PublishEvent(Event)
}

// EventBus broadcasts best-effort live notifications. A full subscriber
// buffer drops its oldest item; the durable event stream remains authoritative.
type EventBus struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan Event
}

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[uint64]chan Event)}
}

func (b *EventBus) Subscribe(ctx context.Context, capacity int) <-chan Event {
	if capacity < 1 {
		capacity = 1
	}
	channel := make(chan Event, capacity)
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subscribers[id] = channel
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if current, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(current)
		}
		b.mu.Unlock()
	}()
	return channel
}

func (b *EventBus) PublishEvent(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, channel := range b.subscribers {
		select {
		case channel <- event:
		default:
			select {
			case <-channel:
			default:
			}
			select {
			case channel <- event:
			default:
			}
		}
	}
}
