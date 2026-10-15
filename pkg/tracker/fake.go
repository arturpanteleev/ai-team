package tracker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// FakeName is the adapter name of the in-memory tracker.
const FakeName = "fake"

// DefaultFakeStatuses is the workflow of a fake tracker without configuration.
var DefaultFakeStatuses = []string{"To Do", "In Progress", "In Review", "Done"}

// FakeIssueSeed describes an issue the fake tracker starts with.
type FakeIssueSeed struct {
	Key, Summary, Status string
}

// FakeCall is one recorded call, for assertions in tests and demos.
type FakeCall struct {
	Op, Key, Arg string
}

type fakeIssue struct {
	key, summary, status string
	comments             []fakeComment
}

type fakeComment struct {
	key   string
	lines []string
}

type fakeFailure struct {
	err       error
	remaining int // < 0: until cleared
}

// Fake is a deterministic in-memory Tracker. Any status may move to any other
// listed status. Every call is recorded; failures can be injected per
// operation. Its clock is a counter, so output never depends on wall time.
type Fake struct {
	mu       sync.Mutex
	statuses []string
	issues   map[string]*fakeIssue
	calls    []FakeCall
	failures map[string]*fakeFailure
	changes  []Change
	tick     int64
}

// NewFake returns a fake tracker with the given statuses (default workflow
// when empty) and seed issues. An issue without a status starts in the first
// status.
func NewFake(statuses []string, seeds ...FakeIssueSeed) *Fake {
	if len(statuses) == 0 {
		statuses = DefaultFakeStatuses
	}
	fake := &Fake{statuses: append([]string(nil), statuses...), issues: map[string]*fakeIssue{}, failures: map[string]*fakeFailure{}}
	for _, seed := range seeds {
		fake.AddIssue(seed)
	}
	return fake
}

func openFake(cfg *config.TrackerConfig, _ Deps) (Tracker, error) {
	var statuses []string
	var seeds []FakeIssueSeed
	if cfg != nil && cfg.Fake != nil {
		statuses = cfg.Fake.Statuses
		for _, issue := range cfg.Fake.Issues {
			seeds = append(seeds, FakeIssueSeed{Key: issue.Key, Summary: issue.Summary, Status: issue.Status})
		}
	}
	return NewFake(statuses, seeds...), nil
}

// Name implements Tracker.
func (f *Fake) Name() string { return FakeName }

// ValidateKey implements Tracker.
func (f *Fake) ValidateKey(key string) error {
	if !issueKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: expected PROJECT-123", ErrInvalidKey)
	}
	return nil
}

// KeyFromLink implements LinkParser: only a bare key is recognized.
func (f *Fake) KeyFromLink(link string) (string, bool) {
	link = strings.TrimSpace(link)
	return link, issueKeyPattern.MatchString(link)
}

// AddIssue creates or resets an issue.
func (f *Fake) AddIssue(seed FakeIssueSeed) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := seed.Status
	if status == "" {
		status = f.statuses[0]
	}
	f.issues[seed.Key] = &fakeIssue{key: seed.Key, summary: seed.Summary, status: status}
}

// FailNext makes the next count calls of op (get, transitions, transition,
// comment, changes) fail with err; count < 0 fails until ClearFailures.
func (f *Fake) FailNext(op string, err error, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[op] = &fakeFailure{err: err, remaining: count}
}

// ClearFailures removes every injected failure.
func (f *Fake) ClearFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = map[string]*fakeFailure{}
}

// ExternalTransition simulates a person changing the status in the tracker.
func (f *Fake) ExternalTransition(key, status, author string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue, ok := f.issues[key]
	if !ok {
		return ErrNotFound
	}
	f.recordChange(issue, status, author, OriginExternal)
	return nil
}

// Calls returns a copy of the recorded calls.
func (f *Fake) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall(nil), f.calls...)
}

// Status returns the current status of an issue.
func (f *Fake) Status(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue, ok := f.issues[key]
	if !ok {
		return "", false
	}
	return issue.status, true
}

// Comments returns the comment bodies posted to an issue, oldest first.
func (f *Fake) Comments(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue, ok := f.issues[key]
	if !ok {
		return nil
	}
	bodies := make([]string, 0, len(issue.comments))
	for _, comment := range issue.comments {
		bodies = append(bodies, strings.Join(comment.lines, "\n"))
	}
	return bodies
}

// begin records the call and applies injected failures. The caller holds mu.
func (f *Fake) begin(op, key, arg string) error {
	f.calls = append(f.calls, FakeCall{Op: op, Key: key, Arg: arg})
	failure := f.failures[op]
	if failure == nil || failure.remaining == 0 {
		return nil
	}
	if failure.remaining > 0 {
		failure.remaining--
	}
	return failure.err
}

func (f *Fake) issue(key string) (*fakeIssue, error) {
	issue, ok := f.issues[key]
	if !ok {
		return nil, ErrNotFound
	}
	return issue, nil
}

// GetIssue implements Tracker.
func (f *Fake) GetIssue(_ context.Context, key string) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("get", key, ""); err != nil {
		return Issue{}, err
	}
	issue, err := f.issue(key)
	if err != nil {
		return Issue{}, err
	}
	return Issue{Key: issue.key, Summary: issue.summary, Status: issue.status, URL: "fake://" + issue.key}, nil
}

// Transitions implements Tracker.
func (f *Fake) Transitions(_ context.Context, key string) ([]Transition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("transitions", key, ""); err != nil {
		return nil, err
	}
	issue, err := f.issue(key)
	if err != nil {
		return nil, err
	}
	var moves []Transition
	for index, status := range f.statuses {
		if status != issue.status {
			moves = append(moves, Transition{ID: strconv.Itoa(index + 1), Name: status, To: status})
		}
	}
	return moves, nil
}

// Transition implements Tracker.
func (f *Fake) Transition(_ context.Context, key, target string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("transition", key, target); err != nil {
		return false, err
	}
	issue, err := f.issue(key)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(issue.status, target) {
		return false, nil
	}
	for _, status := range f.statuses {
		if strings.EqualFold(status, target) {
			f.recordChange(issue, status, "ai-team", OriginAITeam)
			return true, nil
		}
	}
	return false, fmt.Errorf("%w %q", ErrNoTransition, target)
}

func (f *Fake) recordChange(issue *fakeIssue, to, author, origin string) {
	f.tick++
	f.changes = append(f.changes, Change{
		ID: strconv.FormatInt(f.tick, 10), IssueKey: issue.key, From: issue.status, To: to,
		At: time.Unix(f.tick, 0).UTC(), Author: author, Origin: origin,
	})
	issue.status = to
}

// AddComment implements Tracker.
func (f *Fake) AddComment(_ context.Context, key string, comment Comment) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("comment", key, comment.Key); err != nil {
		return false, err
	}
	issue, err := f.issue(key)
	if err != nil {
		return false, err
	}
	if comment.Key != "" {
		for _, existing := range issue.comments {
			if existing.key == comment.Key {
				return false, nil
			}
		}
	}
	issue.comments = append(issue.comments, fakeComment{key: comment.Key, lines: append([]string(nil), comment.Lines...)})
	return true, nil
}

// Changes implements Tracker. The cursor is the sequence number of the last
// change returned; an empty cursor starts at the current tail.
func (f *Fake) Changes(_ context.Context, query ChangeQuery) (ChangePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("changes", "", query.Cursor); err != nil {
		return ChangePage{}, err
	}
	if query.Cursor == "" {
		return ChangePage{Cursor: strconv.FormatInt(f.tick, 10)}, nil
	}
	after, err := strconv.ParseInt(query.Cursor, 10, 64)
	if err != nil || after < 0 {
		return ChangePage{}, errors.New("invalid tracker change cursor")
	}
	wanted := map[string]bool{}
	for _, key := range query.Keys {
		wanted[key] = true
	}
	page := ChangePage{Cursor: query.Cursor}
	for _, change := range f.changes {
		sequence, _ := strconv.ParseInt(change.ID, 10, 64)
		if sequence <= after || (len(wanted) > 0 && !wanted[change.IssueKey]) {
			continue
		}
		if query.Limit > 0 && len(page.Changes) >= query.Limit {
			break
		}
		page.Changes = append(page.Changes, change)
		page.Cursor = change.ID
	}
	return page, nil
}
