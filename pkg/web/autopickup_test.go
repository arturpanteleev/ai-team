package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// fixedLedger — фейковый ledger; параллельные тики подборщика зовут его
// конкурентно, поэтому запись calls защищена мьютексом.
type fixedLedger struct {
	usage tokenbudget.Usage
	err   error
	mu    sync.Mutex
	calls []time.Time
}

func (l *fixedLedger) TokensBetween(start, _ time.Time) (tokenbudget.Usage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, start)
	return l.usage, l.err
}

var pickupTestNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func pickupPolicy(parallel int, daily int64) autopickup.Policy {
	return autopickup.Policy{Enabled: true, MaxParallelTasks: parallel, MaxTaskTokens: 1000, MaxDailyTokens: daily}
}

func newPickupServer(t *testing.T, dbPath, target string, policy autopickup.Policy, ledger tokenbudget.Ledger, now time.Time) (*Server, *taskCreateCapturingController) {
	t.Helper()
	controller := &taskCreateCapturingController{fakeRunController: &fakeRunController{}}
	srv, err := NewServer(dbPath, "", filepath.Join(target, ".ai-team", "artifacts"),
		WithTargetDir(target), WithRunController(controller),
		WithAutoPickup(policy, time.Hour, ledger), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, controller
}

func readyItem(t *testing.T, srv *Server, title string) store.BacklogItem {
	t.Helper()
	item, err := srv.store.CreateBacklogItem(title, "Идея: "+title, "auto-"+strings.ToLower(title), "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	item, err = srv.store.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func eventsOfType(t *testing.T, srv *Server, eventType string) []store.Event {
	t.Helper()
	all, err := srv.store.GetEventsAfter(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var matched []store.Event
	for _, event := range all {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func TestAutoPickupStartsReadyItemInAutoMode(t *testing.T) {
	ledger := &fixedLedger{usage: tokenbudget.Usage{Tokens: 99}}
	srv, controller := newPickupServer(t, ":memory:", t.TempDir(), pickupPolicy(1, 100), ledger, pickupTestNow)
	draft, err := srv.store.CreateBacklogItem("Черновик", "", "draft-idea", "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	item := readyItem(t, srv, "Ready")

	decision, err := srv.runAutoPickup(context.Background())
	if err != nil || !decision.Pick {
		t.Fatalf("pickup decision = %+v err=%v", decision, err)
	}
	if controller.startCalls != 1 || controller.mode != "auto" || controller.feature != item.Feature ||
		controller.title != item.Title || controller.idea != item.Idea {
		t.Fatalf("controller start: calls=%d mode=%q feature=%q title=%q idea=%q", controller.startCalls, controller.mode, controller.feature, controller.title, controller.idea)
	}
	started, err := srv.store.GetBacklogItem(item.ID)
	if err != nil || started.Status != autopickup.StatusStarted || started.RunID != "run-created" {
		t.Fatalf("picked item = %+v err=%v", started, err)
	}
	if got := eventsOfType(t, srv, autopickup.EventStarted); len(got) != 1 || got[0].RunID != "run-created" ||
		!strings.Contains(got[0].DataJSON, fmt.Sprintf(`"item_id":%d`, item.ID)) {
		t.Fatalf("auto_pickup_started events = %+v", got)
	}
	if queued := eventsOfType(t, srv, "run_queued"); len(queued) != 1 || !strings.Contains(queued[0].DataJSON, `"actor_id":"auto-pickup"`) {
		t.Fatalf("auto-picked task must be queued by the pickup actor: %+v", queued)
	}
	if len(ledger.calls) != 1 || !ledger.calls[0].Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily ledger must be read for the UTC day: %v", ledger.calls)
	}

	// Черновик в очередь не попадает: очередь пуста, хотя элемент есть.
	decision, err = srv.runAutoPickup(context.Background())
	if err != nil || decision.Pick || decision.Reason != autopickup.ReasonNoReadyItems || controller.startCalls != 1 {
		t.Fatalf("draft must not be picked: decision=%+v err=%v calls=%d", decision, err, controller.startCalls)
	}
	if stored, _ := srv.store.GetBacklogItem(draft.ID); stored.Status != autopickup.StatusDraft {
		t.Fatalf("draft changed: %+v", stored)
	}
}

func TestAutoPickupLimitsKeepItemQueuedWithEvent(t *testing.T) {
	cases := []struct {
		name       string
		parallel   int
		active     int
		daily      tokenbudget.Usage
		dailyLimit int64
		wantPick   bool
		wantReason string
	}{
		{"one slot below parallel limit", 2, 1, tokenbudget.Usage{}, 100, true, ""},
		{"exactly at parallel limit", 1, 1, tokenbudget.Usage{}, 100, false, autopickup.ReasonParallelLimit},
		{"one above parallel limit", 1, 2, tokenbudget.Usage{}, 100, false, autopickup.ReasonParallelLimit},
		{"one token below daily budget", 1, 0, tokenbudget.Usage{Tokens: 99}, 100, true, ""},
		{"exactly at daily budget", 1, 0, tokenbudget.Usage{Tokens: 100}, 100, false, autopickup.ReasonDailyBudget},
		{"one token above daily budget", 1, 0, tokenbudget.Usage{Tokens: 101}, 100, false, autopickup.ReasonDailyBudget},
		{"unknown daily usage", 1, 0, tokenbudget.Usage{Unknown: true}, 100, false, autopickup.ReasonDailyBudget},
		{"budgets not configured", 1, 0, tokenbudget.Usage{}, 0, false, autopickup.ReasonBudgetNotConfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, controller := newPickupServer(t, ":memory:", t.TempDir(), pickupPolicy(tc.parallel, tc.dailyLimit), &fixedLedger{usage: tc.daily}, pickupTestNow)
			for index := 0; index < tc.active; index++ {
				// Принятая, но не начатая задача без lifecycle занимает слот.
				if err := srv.store.CreatePipelineRun(&store.PipelineRun{
					RunID: fmt.Sprintf("run-active-%d", index), Feature: "busy", Status: "queued", StartedAt: pickupTestNow,
				}); err != nil {
					t.Fatal(err)
				}
			}
			// Завершённые задачи слот не занимают.
			if err := srv.store.CreatePipelineRun(&store.PipelineRun{
				RunID: "run-finished", Feature: "done", Status: string(workflow.RunCompleted), StartedAt: pickupTestNow,
			}); err != nil {
				t.Fatal(err)
			}
			item := readyItem(t, srv, "Limited")
			for round := 0; round < 2; round++ {
				decision, err := srv.runAutoPickup(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if round == 0 && (decision.Pick != tc.wantPick || decision.Reason != tc.wantReason) {
					t.Fatalf("decision = %+v, want pick=%t reason=%q", decision, tc.wantPick, tc.wantReason)
				}
			}
			stored, err := srv.store.GetBacklogItem(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			deferred := eventsOfType(t, srv, autopickup.EventDeferred)
			if tc.wantPick {
				if stored.Status != autopickup.StatusStarted || controller.startCalls != 1 || len(deferred) != 0 {
					t.Fatalf("item=%+v calls=%d deferred=%d", stored, controller.startCalls, len(deferred))
				}
				return
			}
			if stored.Status != autopickup.StatusReady || controller.startCalls != 0 {
				t.Fatalf("limited item must stay queued: item=%+v calls=%d", stored, controller.startCalls)
			}
			// Повторный тик не плодит одинаковые события.
			if len(deferred) != 1 || deferred[0].Severity != "warning" ||
				!strings.Contains(deferred[0].DataJSON, `"reason":"`+tc.wantReason+`"`) ||
				!strings.Contains(deferred[0].DataJSON, `"day":"2026-10-12"`) {
				t.Fatalf("auto_pickup_deferred events = %+v", deferred)
			}
		})
	}
}

func TestAutoPickupDisabledDoesNothing(t *testing.T) {
	srv, controller := newPickupServer(t, ":memory:", t.TempDir(), autopickup.Policy{}, &fixedLedger{}, pickupTestNow)
	item := readyItem(t, srv, "Disabled")
	decision, err := srv.runAutoPickup(context.Background())
	if err != nil || decision.Reason != autopickup.ReasonDisabled || controller.startCalls != 0 {
		t.Fatalf("disabled pickup: decision=%+v err=%v calls=%d", decision, err, controller.startCalls)
	}
	if stored, _ := srv.store.GetBacklogItem(item.ID); stored.Status != autopickup.StatusReady {
		t.Fatalf("item changed: %+v", stored)
	}
	if len(eventsOfType(t, srv, autopickup.EventDeferred)) != 0 {
		t.Fatal("disabled pickup must stay silent")
	}
}

func TestAutoPickupLedgerErrorFailsClosed(t *testing.T) {
	srv, controller := newPickupServer(t, ":memory:", t.TempDir(), pickupPolicy(1, 100), &fixedLedger{err: fmt.Errorf("disk")}, pickupTestNow)
	readyItem(t, srv, "Ledger")
	if _, err := srv.runAutoPickup(context.Background()); err == nil || controller.startCalls != 0 {
		t.Fatalf("ledger failure must prevent pickup: err=%v calls=%d", err, controller.startCalls)
	}
}

func TestAutoPickupStartFailureIsNotRetried(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(&fakeRunController{}),
		WithAutoPickup(pickupPolicy(1, 100), time.Hour, &fixedLedger{}), WithClock(func() time.Time { return pickupTestNow }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	item := readyItem(t, srv, "Unsupported")
	decision, err := srv.runAutoPickup(context.Background())
	if err != nil || decision.Pick {
		t.Fatalf("decision = %+v err=%v", decision, err)
	}
	stored, _ := srv.store.GetBacklogItem(item.ID)
	if stored.Status != autopickup.StatusFailed || stored.Error == "" {
		t.Fatalf("controller without auto mode must fail the item: %+v", stored)
	}
	if failed := eventsOfType(t, srv, autopickup.EventFailed); len(failed) != 1 {
		t.Fatalf("auto_pickup_failed events = %+v", failed)
	}
	if decision, _ := srv.runAutoPickup(context.Background()); decision.Reason != autopickup.ReasonNoReadyItems {
		t.Fatalf("failed item must not be retried: %+v", decision)
	}
}

func writePickupAttemptManifest(t *testing.T, target, runID string, finishedAt time.Time, tokens int64) {
	t.Helper()
	dir := filepath.Join(target, ".ai-team", "runs", runID, "attempts", "attempt-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"started_at": finishedAt.Add(-time.Minute), "finished_at": finishedAt,
		"usage": map[string]any{"attested": true, "tokens_input": tokens, "tokens_output": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Дневной счётчик пересчитывается из evidence: рестарт процесса его не
// обнуляет, а смена суток UTC — обнуляет.
func TestAutoPickupDailyCounterSurvivesRestartAndResetsAtUTCMidnight(t *testing.T) {
	target := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "web.db")
	writePickupAttemptManifest(t, target, "20261012T080000Z-spent", pickupTestNow.Add(-time.Hour), 100)
	policy := pickupPolicy(1, 100)

	first, controller := newPickupServer(t, dbPath, target, policy, nil, pickupTestNow)
	readyItem(t, first, "Budget")
	decision, err := first.runAutoPickup(context.Background())
	if err != nil || decision.Reason != autopickup.ReasonDailyBudget || controller.startCalls != 0 {
		t.Fatalf("before restart: decision=%+v err=%v calls=%d", decision, err, controller.startCalls)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	lastSecond := time.Date(2026, 10, 12, 23, 59, 59, 0, time.UTC)
	restarted, controller := newPickupServer(t, dbPath, target, policy, nil, lastSecond)
	decision, err = restarted.runAutoPickup(context.Background())
	if err != nil || decision.Reason != autopickup.ReasonDailyBudget || controller.startCalls != 0 {
		t.Fatalf("after restart the daily budget must still be spent: decision=%+v err=%v", decision, err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	nextDay := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	tomorrow, controller := newPickupServer(t, dbPath, target, policy, nil, nextDay)
	decision, err = tomorrow.runAutoPickup(context.Background())
	if err != nil || !decision.Pick || controller.startCalls != 1 {
		t.Fatalf("new UTC day must reset the daily budget: decision=%+v err=%v calls=%d", decision, err, controller.startCalls)
	}
}

func TestReconcileBacklogClaimsAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "web.db")
	target := t.TempDir()
	srv, _ := newPickupServer(t, dbPath, target, autopickup.Policy{}, &fixedLedger{}, pickupTestNow)
	admitted := readyItem(t, srv, "Admitted")
	orphan := readyItem(t, srv, "Orphan")
	for _, item := range []store.BacklogItem{admitted, orphan} {
		if claimed, err := srv.store.ClaimBacklogItem(item.ID, pickupTestNow); err != nil || !claimed {
			t.Fatal(claimed, err)
		}
	}
	if err := srv.store.BindBacklogRun(admitted.ID, "run-admitted", pickupTestNow); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreatePipelineRun(&store.PipelineRun{RunID: "run-admitted", Feature: "admitted", Status: "queued", StartedAt: pickupTestNow}); err != nil {
		t.Fatal(err)
	}
	if active, err := srv.activeTaskCount(); err != nil || active != 2 {
		t.Fatalf("admitted run and unbound claim occupy slots: active=%d err=%v", active, err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newPickupServer(t, dbPath, target, autopickup.Policy{}, &fixedLedger{}, pickupTestNow)
	if item, _ := restarted.store.GetBacklogItem(admitted.ID); item.Status != autopickup.StatusStarted {
		t.Fatalf("admitted claim = %+v", item)
	}
	if item, _ := restarted.store.GetBacklogItem(orphan.ID); item.Status != autopickup.StatusFailed || item.Error == "" {
		t.Fatalf("orphan claim must fail closed: %+v", item)
	}
}

func TestBacklogHTTPAPI(t *testing.T) {
	srv, _ := newPickupServer(t, ":memory:", t.TempDir(), pickupPolicy(1, 100), &fixedLedger{}, pickupTestNow)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, authorizedRequest(t, srv, method, path, body))
		return response
	}
	created := serve(http.MethodPost, "/api/backlog", `{"title":"Новая идея","idea":"Сделать отчёт","feature":"report-idea"}`)
	var item store.BacklogItem
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &item) != nil || item.Status != autopickup.StatusDraft {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, body := range []string{`{"title":"","feature":"x"}`, `{"title":"ok","feature":"../bad"}`, `{"title":"ok","feature":"x","status":"ready"}`} {
		if response := serve(http.MethodPost, "/api/backlog", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid create %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	statusPath := fmt.Sprintf("/api/backlog/%d/status", item.ID)
	if response := serve(http.MethodPost, statusPath, `{"status":"auto"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d %s", response.Code, response.Body.String())
	}
	if response := serve(http.MethodPost, statusPath, `{"status":"started"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("controller-owned status: %d %s", response.Code, response.Body.String())
	}
	ready := serve(http.MethodPost, statusPath, `{"status":"ready"}`)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"status":"ready"`) || !strings.Contains(ready.Body.String(), `"ready_by":"local-user"`) {
		t.Fatalf("mark ready: %d %s", ready.Code, ready.Body.String())
	}
	if response := serve(http.MethodPost, statusPath, `{"status":"ready"}`); response.Code != http.StatusConflict {
		t.Fatalf("repeat ready must conflict: %d %s", response.Code, response.Body.String())
	}
	if response := serve(http.MethodPost, "/api/backlog/999/status", `{"status":"ready"}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing item: %d %s", response.Code, response.Body.String())
	}
	if response := serve(http.MethodPost, "/api/backlog/x/status", `{"status":"ready"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d %s", response.Code, response.Body.String())
	}
	list := serve(http.MethodGet, "/api/backlog", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"max_parallel_tasks":1`) || !strings.Contains(list.Body.String(), `"title":"Новая идея"`) {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	unauthenticated := httptest.NewRecorder()
	srv.router.ServeHTTP(unauthenticated, newLoopbackRequest(http.MethodPost, "/api/backlog", strings.NewReader(`{"title":"x","feature":"x"}`)))
	if unauthenticated.Code < 400 {
		t.Fatalf("write without session must be rejected: %d", unauthenticated.Code)
	}
}

// N подборщиков (тиков или процессов на одной базе) на одном элементе:
// захват выигрывает ровно один, задача создаётся ровно одна.
func TestAutoPickupClaimRaceStartsExactlyOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "web.db")
	srv, controller := newPickupServer(t, dbPath, t.TempDir(), pickupPolicy(8, 100), &fixedLedger{}, pickupTestNow)
	item := readyItem(t, srv, "Race")
	const pickers = 8
	results := make(chan bool, pickers)
	start := make(chan struct{})
	for index := 0; index < pickers; index++ {
		go func() {
			<-start
			claimed, err := srv.store.ClaimBacklogItem(item.ID, pickupTestNow)
			if err != nil {
				t.Error(err)
			}
			results <- claimed
		}()
	}
	close(start)
	winners := 0
	for index := 0; index < pickers; index++ {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want exactly 1", winners)
	}

	// Тот же инвариант на полном шаге подборщика: параллельные тики
	// запускают задачу один раз.
	second := readyItem(t, srv, "RaceTick")
	ticks := make(chan error, pickers)
	begin := make(chan struct{})
	for index := 0; index < pickers; index++ {
		go func() {
			<-begin
			_, err := srv.runAutoPickup(context.Background())
			ticks <- err
		}()
	}
	close(begin)
	for index := 0; index < pickers; index++ {
		if err := <-ticks; err != nil {
			t.Fatal(err)
		}
	}
	if controller.startCalls != 1 {
		t.Fatalf("concurrent ticks started %d tasks, want 1", controller.startCalls)
	}
	if stored, _ := srv.store.GetBacklogItem(second.ID); stored.Status != autopickup.StatusStarted {
		t.Fatalf("raced item = %+v", stored)
	}
}

func TestResumeRecordsActorAndBudgetAllowance(t *testing.T) {
	srv, controller := newPickupServer(t, ":memory:", t.TempDir(), autopickup.Policy{}, &fixedLedger{}, pickupTestNow)
	if err := srv.store.CreatePipelineRun(&store.PipelineRun{RunID: "run-budget", Feature: "budget", Status: "stopped", StartedAt: pickupTestNow}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AppendEventNext(&store.Event{RunID: "run-budget", Type: "budget_exceeded", Timestamp: pickupTestNow, DataJSON: `{"scope":"task"}`}); err != nil {
		t.Fatal(err)
	}
	resume := func() store.Event {
		t.Helper()
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodPost, "/api/runs/run-budget/resume", ""))
		if response.Code != http.StatusAccepted {
			t.Fatalf("resume: %d %s", response.Code, response.Body.String())
		}
		latest, err := srv.store.LatestRunEvent("run-budget", "run_resume_requested")
		if err != nil || latest == nil {
			t.Fatalf("run_resume_requested missing: %v", err)
		}
		return *latest
	}
	first := resume()
	if controller.resumeRunID != "run-budget" || !strings.Contains(first.DataJSON, `"actor_id":"local-user"`) ||
		!strings.Contains(first.DataJSON, `"budget_allowance_granted":true`) {
		t.Fatalf("first resume after budget stop = %+v", first)
	}
	if second := resume(); !strings.Contains(second.DataJSON, `"budget_allowance_granted":false`) {
		t.Fatalf("resume without a new budget stop must not claim a grant: %+v", second)
	}
	// Дневной стоп resume новым бюджетом не снимает.
	if err := srv.store.AppendEventNext(&store.Event{RunID: "run-budget", Type: "budget_exceeded", Timestamp: pickupTestNow, DataJSON: `{"scope":"daily"}`}); err != nil {
		t.Fatal(err)
	}
	if third := resume(); !strings.Contains(third.DataJSON, `"budget_allowance_granted":false`) {
		t.Fatalf("daily budget stop must not report a task allowance grant: %+v", third)
	}
}
