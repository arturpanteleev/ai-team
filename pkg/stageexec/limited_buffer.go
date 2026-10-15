package stageexec

import (
	"bytes"
	"encoding/json"
	"sync"
)

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

// LimitedBuffer keeps at most limit bytes and silently drops the rest while
// reporting truncation, so a noisy process cannot exhaust controller memory.
type LimitedBuffer struct {
	mu        sync.Mutex
	limit     int64
	buf       bytes.Buffer
	truncated bool
}

// NewLimitedBuffer returns a buffer that keeps at most limit bytes.
func NewLimitedBuffer(limit int64) *LimitedBuffer { return &LimitedBuffer{limit: limit} }

// Write implements io.Writer; it never fails so the child is not killed by SIGPIPE.
func (b *LimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - int64(b.buf.Len())
	if room > 0 {
		if int64(len(p)) <= room {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:room])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// Bytes returns the retained bytes.
func (b *LimitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// Truncated reports whether output beyond the limit was dropped.
func (b *LimitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Limit reports the retention limit in bytes.
func (b *LimitedBuffer) Limit() int64 { return b.limit }
