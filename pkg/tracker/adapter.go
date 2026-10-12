// Package tracker contains one-way external task tracker adapters.
package tracker

import (
	"context"
	"time"
)

// TaskEvent is an immutable task event enriched with the task's external links
// and stable web identity before it is delivered to an adapter.
type TaskEvent struct {
	Cursor        int64
	RunID         string
	TaskID        int64
	Type          string
	Stage         string
	AttemptID     string
	Timestamp     time.Time
	Data          map[string]any
	ExternalLinks []string
}

// Adapter handles one task event. Implementations run outside the pipeline;
// errors are recorded as durable integration_error events by the subscriber.
type Adapter interface {
	HandleTaskEvent(context.Context, TaskEvent) error
}
