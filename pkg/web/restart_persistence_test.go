package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// waitingStageEngine — run engine, который ведёт себя как pipeline на первом
// этапе: проецирует run_started/attempt_* в SQLite через отдельное соединение
// (как recorderStore в `ai-team web`), открывает человеческий запрос на
// подтверждение этапа и паркует run в фазе waiting. done закрывается, когда
// вся durable-запись сделана, — тест ждёт его вместо time.Sleep.
type waitingStageEngine struct {
	t        *testing.T
	target   string
	recorder *StoreRecorder
	stageID  string
	done     chan struct{}
}

func (e *waitingStageEngine) Start(_ context.Context, cfg pipeline.RunConfig) (pipeline.RunResult, error) {
	defer close(e.done)
	started := time.Now().UTC().Truncate(time.Second)
	e.recorder.RunStarted(cfg.RunID, cfg.Feature, `{"task":"`+cfg.TaskDesc+`"}`, started)
	attemptID := cfg.RunID + "-001-" + e.stageID
	e.recorder.StageStarted(cfg.RunID, attemptID, e.stageID, 1, started)
	e.recorder.StageFinished(notifier.StageResult{RunID: cfg.RunID, AttemptID: attemptID, Name: e.stageID,
		Status: notifier.StatusPassed, StartedAt: started, FinishedAt: started.Add(time.Second)})

	approvals, err := approval.NewStore(e.target)
	if err != nil {
		e.t.Error(err)
		return pipeline.RunResult{}, err
	}
	pending, err := approvals.Create(approval.PendingApproval{
		RunID: cfg.RunID, AttemptID: attemptID, FromStage: e.stageID, ToStage: "$stop",
		Trigger: "stage_completed", SubjectHash: testSubjectHash,
		RequiredRoles: []string{"product_owner"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "$stop", "reject": "$stop"},
	})
	if err != nil {
		e.t.Error(err)
		return pipeline.RunResult{}, err
	}
	e.recorder.ApprovalRequested(cfg.RunID, pending.ID, attemptID, started.Add(time.Second), map[string]any{"approval_id": pending.ID})
	states, err := lifecycle.NewStore(e.target)
	if err != nil {
		e.t.Error(err)
		return pipeline.RunResult{}, err
	}
	if err := states.Create(lifecycle.State{
		RunID: cfg.RunID, Feature: cfg.Feature, TargetDir: e.target, Task: cfg.TaskDesc,
		Phase: lifecycle.PhaseWaiting, NextStage: e.stageID, PendingApprovalID: pending.ID,
		ConfigSHA256: strings.Repeat("0", 64), WorkflowSHA256: strings.Repeat("1", 64), CreatedAt: started,
	}); err != nil {
		e.t.Error(err)
		return pipeline.RunResult{}, err
	}
	e.recorder.RunPaused(cfg.RunID, "waiting_for_approval", started.Add(2*time.Second))
	return pipeline.RunResult{RunID: cfg.RunID}, pipeline.ErrUserStopped
}

func (e *waitingStageEngine) Resume(context.Context, pipeline.ResumeConfig) (pipeline.RunResult, error) {
	e.t.Error("перезапуск сервера не должен возобновлять run")
	return pipeline.RunResult{}, nil
}

func (e *waitingStageEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{RunID: config.RunID}, nil
}

// restartServer поднимает web-сервер над файловой базой dbPath и целевым
// каталогом target — так же, как `ai-team web` при каждом старте процесса.
func restartServer(t *testing.T, dbPath, target string, engine *waitingStageEngine) *Server {
	t.Helper()
	controller, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(dbPath, "", filepath.Join(target, ".ai-team", "artifacts"),
		WithTargetDir(target), WithRunController(controller))
	if err != nil {
		t.Fatalf("старт сервера над %s: %v", dbPath, err)
	}
	controller.SetFailureSink(srv.RecordAdmissionFailure)
	return srv
}

func getJSON(t *testing.T, srv *Server, path string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodGet, path, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}

// BL-12 (§12): перезапуск сервера не теряет задачи, запросы и события.
// Задача создаётся через HTTP на настоящем control.Controller, сервер
// закрывается вместе с базой, новый сервер поднимается над теми же файлами и
// обязан отдать ту же задачу, тот же человеческий запрос и ту же ленту
// событий; повтор create-запроса с тем же Idempotency-Key — точный replay без
// второго запуска, а новые события продолжают курсор, а не начинают его с нуля.
func TestServerRestartKeepsTasksRequestsAndEvents(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(filepath.Join(controlDir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	templateYAML, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), templateYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(controlDir, "web.db")

	// Процесс 1: отдельное соединение рекордера, как recorderStore в main.go.
	recorderStore, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first := &waitingStageEngine{t: t, target: target, recorder: NewStoreRecorder(recorderStore),
		stageID: cfg.Stages[0].ID, done: make(chan struct{})}
	srv := restartServer(t, dbPath, target, first)

	const createBody = `{"feature":"restart-task","task":"Задача переживает перезапуск"}`
	create := authorizedRequest(t, srv, http.MethodPost, "/api/runs", createBody)
	create.Header.Set("Idempotency-Key", "create-restart-task")
	created := httptest.NewRecorder()
	srv.router.ServeHTTP(created, create)
	if created.Code != http.StatusAccepted {
		t.Fatalf("создание задачи: status=%d body=%s", created.Code, created.Body.String())
	}
	var admitted struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &admitted); err != nil || admitted.RunID == "" {
		t.Fatalf("ответ создания без run_id: %s err=%v", created.Body.String(), err)
	}
	<-first.done

	runID := admitted.RunID
	beforeList := getJSON(t, srv, "/api/runs")
	beforeDetail := getJSON(t, srv, "/api/runs/"+runID)
	beforeEvents := getJSON(t, srv, "/api/runs/"+runID+"/events?after=0")
	beforeGlobal := getJSON(t, srv, "/api/events?after=0")

	var detail taskRunDetails
	if err := json.Unmarshal(beforeDetail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run == nil || detail.Run.Feature != "restart-task" || detail.Run.Status != "waiting_for_approval" ||
		detail.State == nil || detail.State.Phase != lifecycle.PhaseWaiting || len(detail.Approvals) != 1 ||
		detail.Approvals[0].Status != approval.StatusPending || detail.State.PendingApprovalID != detail.Approvals[0].ID {
		t.Fatalf("до перезапуска задача должна ждать человека: %s", beforeDetail)
	}
	for _, eventType := range []string{"run_queued", "run_started", "attempt_started", "attempt_finished", "approval_requested", "run_paused"} {
		if !bytes.Contains(beforeEvents, []byte(`"type":"`+eventType+`"`)) {
			t.Fatalf("до перезапуска нет события %s: %s", eventType, beforeEvents)
		}
	}

	// Остановка процесса: сервер и соединение рекордера закрываются.
	if err := srv.Close(); err != nil {
		t.Fatalf("остановка сервера: %v", err)
	}
	if err := recorderStore.Close(); err != nil {
		t.Fatal(err)
	}

	// Процесс 2: новый сервер и новый контроллер над той же базой и target.
	second := &waitingStageEngine{t: t, target: target, done: make(chan struct{})}
	restarted := restartServer(t, dbPath, target, second)
	t.Cleanup(func() { _ = restarted.Close() })

	for _, check := range []struct {
		path   string
		before []byte
	}{
		{"/api/runs", beforeList},
		{"/api/runs/" + runID, beforeDetail},
		{"/api/runs/" + runID + "/events?after=0", beforeEvents},
		{"/api/events?after=0", beforeGlobal},
	} {
		if after := getJSON(t, restarted, check.path); !bytes.Equal(after, check.before) {
			t.Errorf("GET %s после перезапуска отличается:\nдо:    %s\nпосле: %s", check.path, check.before, after)
		}
	}

	// Повтор того же запроса после перезапуска — replay, а не вторая задача.
	replay := authorizedRequest(t, restarted, http.MethodPost, "/api/runs", createBody)
	replay.Header.Set("Idempotency-Key", "create-restart-task")
	replayed := httptest.NewRecorder()
	restarted.router.ServeHTTP(replayed, replay)
	if replayed.Code != created.Code || replayed.Body.String() != created.Body.String() {
		t.Fatalf("повтор create после перезапуска должен быть точным replay: first=%d %s retry=%d %s",
			created.Code, created.Body.String(), replayed.Code, replayed.Body.String())
	}
	select {
	case <-second.done:
		t.Fatal("повтор create после перезапуска запустил задачу второй раз")
	default:
	}
	var runs struct {
		Runs []json.RawMessage `json:"runs"`
	}
	list := getJSON(t, restarted, "/api/runs")
	if err := json.Unmarshal(list, &runs); err != nil {
		t.Fatalf("список задач: %v %s", err, list)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("после перезапуска и replay должна остаться ровно одна задача: %s", list)
	}

	// Новое событие после перезапуска продолжает курсор, а не начинает его
	// с нуля поверх старых событий.
	var stored struct {
		NextCursor int64 `json:"next_cursor"`
	}
	if err := json.Unmarshal(beforeEvents, &stored); err != nil || stored.NextCursor == 0 {
		t.Fatalf("лента событий до перезапуска: %s err=%v", beforeEvents, err)
	}
	restarted.appendRunEvent(runID, "restart_probe", time.Now().UTC(), map[string]any{})
	var fresh struct {
		Events []struct {
			Cursor int64  `json:"cursor"`
			Type   string `json:"type"`
		} `json:"events"`
	}
	tail := getJSON(t, restarted, "/api/runs/"+runID+"/events?after="+strconv.FormatInt(stored.NextCursor, 10))
	if err := json.Unmarshal(tail, &fresh); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Events) != 1 || fresh.Events[0].Type != "restart_probe" || fresh.Events[0].Cursor <= stored.NextCursor {
		t.Fatalf("событие после перезапуска должно продолжить курсор после %d: %s", stored.NextCursor, tail)
	}
}
