package notifier

import (
	"context"
	"testing"
	"time"
)

func TestEventBusSubscriptionReceivesLatestEventAndCloses(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	subscription := bus.Subscribe(ctx, 1)
	bus.PublishEvent(Event{RunID: "run-1", Type: "first"})
	bus.PublishEvent(Event{RunID: "run-1", Type: "stage_stalled", Severity: "warning"})

	select {
	case event := <-subscription:
		if event.Type != "stage_stalled" || event.Severity != "warning" {
			t.Fatalf("buffer should retain latest event, got %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not receive event")
	}
	cancel()
	select {
	case _, open := <-subscription:
		if open {
			t.Fatal("subscription should close when its context is canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription was not closed")
	}
}
