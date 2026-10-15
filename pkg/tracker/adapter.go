// Package tracker defines the abstract task tracker (Jira and similar
// systems) that ai-team synchronizes a task with, the adapters behind it and
// the pure outbound planning of stage events into tracker operations.
//
// The package knows nothing about SQLite or HTTP routes: the web layer owns
// the task <-> issue link, the durable delivery log and the cursor, and calls
// into this package to decide and execute operations.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// Sentinel errors every adapter maps its failures onto, so callers can tell a
// missing issue from a permission problem or an impossible transition without
// parsing adapter-specific text.
var (
	ErrInvalidKey   = errors.New("tracker issue key has an invalid format")
	ErrNotFound     = errors.New("tracker issue not found")
	ErrForbidden    = errors.New("tracker access denied")
	ErrNoTransition = errors.New("tracker has no available transition to the requested status")
)

// Issue is the part of a tracker card ai-team needs.
type Issue struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
}

// Transition is one move the tracker currently allows for an issue.
type Transition struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	To   string `json:"to"`
}

// Comment is a tracker comment. Key is the idempotency key: an adapter MUST
// NOT post a second comment carrying a key it has already posted.
type Comment struct {
	Key   string
	Lines []string
}

// ChangeQuery asks for tracker-side changes after an opaque cursor. An empty
// cursor means "from now": the first call returns no changes and the cursor
// of the current tail. Keys limits the issues that are read.
type ChangeQuery struct {
	Cursor string
	Keys   []string
	Limit  int
}

// Change is one status change made in the tracker. Origin is OriginAITeam when
// the change was made by ai-team's own tracker account, so an inbound sync can
// ignore its own outbound transitions instead of looping.
type Change struct {
	ID       string    `json:"id"`
	IssueKey string    `json:"issue_key"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	At       time.Time `json:"at"`
	Author   string    `json:"author,omitempty"`
	Origin   string    `json:"origin"`
}

// OriginAITeam marks changes made by ai-team itself.
const OriginAITeam = "ai-team"

// OriginExternal marks changes made by anyone else.
const OriginExternal = "external"

// ChangePage is a batch of changes in ascending order and the cursor to
// continue from. The cursor is stable: repeating a query with it never
// returns a change twice.
type ChangePage struct {
	Changes []Change `json:"changes"`
	Cursor  string   `json:"cursor"`
}

// Tracker is the abstract task management system. Adapters are named in the
// configuration and found through a Registry.
type Tracker interface {
	// Name is the adapter name used in configuration and in the link record.
	Name() string
	// ValidateKey checks the issue key format without any network request.
	ValidateKey(key string) error
	// GetIssue returns the card, proving that it exists and is accessible.
	GetIssue(ctx context.Context, key string) (Issue, error)
	// Transitions lists the moves currently allowed for the issue.
	Transitions(ctx context.Context, key string) ([]Transition, error)
	// Transition moves the issue to the target status (or transition name).
	// It is idempotent: when the issue is already in the target status it
	// reports changed=false and sends nothing.
	Transition(ctx context.Context, key, target string) (changed bool, err error)
	// AddComment posts the comment unless one with the same key exists.
	AddComment(ctx context.Context, key string, comment Comment) (posted bool, err error)
	// Changes reads status changes after a cursor (used by inbound sync).
	Changes(ctx context.Context, query ChangeQuery) (ChangePage, error)
}

// LinkParser is implemented by adapters that can recognize an issue key in a
// legacy free-form external link. It is used only by the one-time migration
// of existing links.
type LinkParser interface {
	KeyFromLink(link string) (string, bool)
}

// TaskEvent is an immutable task event enriched with the task's stable web
// identity before it is planned into tracker operations.
type TaskEvent struct {
	Cursor    int64
	RunID     string
	TaskID    int64
	Type      string
	Stage     string
	AttemptID string
	Timestamp time.Time
	Data      map[string]any
}

// Deps are the injectable effects an adapter factory may use.
type Deps struct {
	Getenv     func(string) string
	HTTPClient *http.Client
}

// Factory builds an adapter from the tracker configuration.
type Factory func(cfg *config.TrackerConfig, deps Deps) (Tracker, error)

// Registry maps adapter names from the configuration to factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns a registry with the built-in jira and fake adapters.
func NewRegistry() *Registry {
	registry := &Registry{factories: map[string]Factory{}}
	registry.Register("jira", openJira)
	registry.Register("fake", openFake)
	return registry
}

// Register adds or replaces an adapter factory.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Names lists registered adapters in a stable order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Open builds the adapter selected by cfg. A nil cfg yields (nil, nil): no
// tracker is configured.
func (r *Registry) Open(cfg *config.TrackerConfig, deps Deps) (Tracker, error) {
	if cfg == nil {
		return nil, nil
	}
	name := cfg.AdapterName()
	if name == "" {
		return nil, errors.New("tracker adapter is not selected")
	}
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tracker adapter %q is not registered", name)
	}
	if deps.Getenv == nil {
		deps.Getenv = func(string) string { return "" }
	}
	return factory(cfg, deps)
}
