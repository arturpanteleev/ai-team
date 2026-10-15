package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tokenbudget"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// autoPickupActor — исполнитель, от имени которого подборщик создаёт задачу.
const autoPickupActor = "auto-pickup"

// Ограничения полей черновой идеи.
const (
	maxBacklogTitleBytes = 200
	maxBacklogIdeaBytes  = 20000
)

// WithAutoPickup включает подборщик задач из бэклога с политикой инстанса.
// pollInterval задаёт период проверки очереди; ledger — источник дневного
// расхода токенов (nil — пересчёт по evidence target).
func WithAutoPickup(policy autopickup.Policy, pollInterval time.Duration, ledger tokenbudget.Ledger) ServerOption {
	return func(server *Server) {
		server.autoPickup = policy
		server.autoPickupInterval = pollInterval
		server.tokenLedger = ledger
	}
}

func (s *Server) monitorAutoPickup(ctx context.Context) {
	defer s.eventWorkers.Done()
	interval := s.autoPickupInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.runAutoPickup(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "auto pickup: %v\n", err)
			}
		}
	}
}

// runAutoPickup выполняет один шаг подборщика: пересчитывает активные задачи
// и дневной расход из долговечных источников, применяет политику и запускает
// не более одной задачи. Любая ошибка пересчёта — отказ от подбора.
func (s *Server) runAutoPickup(ctx context.Context) (autopickup.Decision, error) {
	now := s.clock().UTC()
	item, hasReady, err := s.store.NextReadyBacklogItem()
	if err != nil {
		return autopickup.Decision{}, fmt.Errorf("read backlog queue: %w", err)
	}
	snapshot := autopickup.Snapshot{HasReady: hasReady, DailyUsage: tokenbudget.Usage{Unknown: true}}
	if hasReady {
		snapshot.ActiveTasks, err = s.activeTaskCount()
		if err != nil {
			return autopickup.Decision{}, fmt.Errorf("count active tasks: %w", err)
		}
		start, end := tokenbudget.DayWindow(now)
		snapshot.DailyUsage, err = s.dailyLedger().TokensBetween(start, end)
		if err != nil {
			return autopickup.Decision{}, fmt.Errorf("daily token ledger: %w", err)
		}
	}
	decision := autopickup.Decide(s.autoPickup, snapshot)
	if !decision.Pick {
		if hasReady && decision.Reason != autopickup.ReasonDisabled {
			if err := s.recordPickupDeferred(item, decision.Reason, snapshot, now); err != nil {
				return decision, err
			}
		}
		return decision, nil
	}
	claimed, err := s.store.ClaimBacklogItem(item.ID, now)
	if err != nil {
		return autopickup.Decision{}, fmt.Errorf("claim backlog item %d: %w", item.ID, err)
	}
	if !claimed {
		return autopickup.Decision{Reason: autopickup.ReasonNoReadyItems}, nil
	}
	command := startRunCommand{ProjectID: item.ProjectID, Feature: item.Feature, Task: item.Title, Title: item.Title, Idea: item.Idea, Mode: "auto"}
	runID, _, _, startErr := s.startTask(ctx, command, autoPickupActor, func(runID string) error {
		return s.store.BindBacklogRun(item.ID, runID, now)
	})
	if startErr != nil {
		if err := s.store.FinishBacklogClaim(item.ID, autopickup.StatusFailed, startErr.Error(), now); err != nil {
			return decision, fmt.Errorf("record failed pickup of backlog item %d: %w", item.ID, err)
		}
		s.appendInstanceEvent(autopickup.EventFailed, now, map[string]any{
			"item_id": item.ID, "error": startErr.Error(), "severity": "warning", "actor": autoPickupActor,
		})
		return autopickup.Decision{Reason: "start_failed"}, nil
	}
	if err := s.store.FinishBacklogClaim(item.ID, autopickup.StatusStarted, "", now); err != nil {
		return decision, fmt.Errorf("record started backlog item %d: %w", item.ID, err)
	}
	s.appendRunEvent(runID, autopickup.EventStarted, now, map[string]any{
		"item_id": item.ID, "ready_by": item.ReadyBy, "actor": autoPickupActor,
		"active_tasks": snapshot.ActiveTasks, "max_parallel_tasks": s.autoPickup.MaxParallelTasks,
		"daily_tokens": snapshot.DailyUsage.Tokens, "max_daily_tokens": s.autoPickup.MaxDailyTokens,
	})
	return decision, nil
}

func (s *Server) dailyLedger() tokenbudget.Ledger {
	if s.tokenLedger != nil {
		return s.tokenLedger
	}
	return tokenbudget.FilesystemLedger{RunsRoot: s.runRoot}
}

// recordPickupDeferred пишет событие отложенного подбора не чаще одного раза
// на элемент, причину и сутки UTC: каждый тик не плодит одинаковые события.
func (s *Server) recordPickupDeferred(item store.BacklogItem, reason string, snapshot autopickup.Snapshot, now time.Time) error {
	day := tokenbudget.Day(now)
	data := map[string]any{
		"item_id": item.ID, "reason": reason, "day": day, "severity": "warning", "actor": autoPickupActor,
		"active_tasks": snapshot.ActiveTasks, "max_parallel_tasks": s.autoPickup.MaxParallelTasks,
		"daily_tokens": snapshot.DailyUsage.Tokens, "daily_usage_unknown": snapshot.DailyUsage.Unknown,
		"max_daily_tokens": s.autoPickup.MaxDailyTokens,
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.store.AppendEventNextIfAbsent(&store.Event{
		Type: autopickup.EventDeferred, Actor: autoPickupActor, Severity: "warning", Timestamp: now,
		DataJSON: string(encoded), DedupeKey: fmt.Sprintf("%s:%s:%d:%s", autopickup.EventDeferred, reason, item.ID, day),
	})
	if err != nil {
		return fmt.Errorf("append %s: %w", autopickup.EventDeferred, err)
	}
	return nil
}

// appendInstanceEvent пишет событие уровня инстанса (без run).
func (s *Server) appendInstanceEvent(eventType string, at time.Time, data map[string]any) {
	encoded, err := json.Marshal(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s event payload: %v\n", eventType, err)
		return
	}
	if err := s.store.AppendEventNext(&store.Event{Type: eventType, Timestamp: at, DataJSON: string(encoded)}); err != nil {
		fmt.Fprintf(os.Stderr, "%s event could not be persisted: %v\n", eventType, err)
	}
}

// activeTaskCount считает незавершённые задачи инстанса по lifecycle
// checkpoint, а не по проекции статуса: пауза и ожидание человека тоже
// занимают слот. Нечитаемый checkpoint считается активным (fail-closed).
func (s *Server) activeTaskCount() (int, error) {
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		return 0, err
	}
	active := 0
	for offset := 0; ; offset += 100 {
		runs, err := s.store.GetPipelineRunsPage(100, offset)
		if err != nil {
			return 0, err
		}
		for _, run := range runs {
			if run.RunID == "" {
				continue
			}
			state, loadErr := stateStore.Load(run.RunID)
			switch {
			case loadErr == nil:
				if state.Phase != lifecycle.PhaseTerminal {
					active++
				}
			case errors.Is(loadErr, fs.ErrNotExist):
				// Принятая, но ещё не начатая задача уже занимает слот.
				if run.Status == "queued" || run.Status == "pending" || run.Status == "running" {
					active++
				}
			default:
				active++
			}
		}
		if len(runs) < 100 {
			break
		}
	}
	starting, err := s.store.StartingBacklogItems()
	if err != nil {
		return 0, err
	}
	for _, item := range starting {
		if item.RunID == "" {
			active++
		}
	}
	return active, nil
}

// reconcileBacklogClaims завершает захваты, прерванные рестартом. Элемент со
// связанным и видимым run считается запущенным; без него — неудачным: вернуть
// его в очередь может только человек, повторного запуска не будет.
func (s *Server) reconcileBacklogClaims(now time.Time) error {
	items, err := s.store.StartingBacklogItems()
	if err != nil {
		return err
	}
	for _, item := range items {
		status, cause := autopickup.StatusFailed, "pickup interrupted before the task was admitted"
		if item.RunID != "" {
			if _, err := s.store.GetPipelineRunByRunID(item.RunID); err == nil {
				status, cause = autopickup.StatusStarted, ""
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := s.store.FinishBacklogClaim(item.ID, status, cause, now); err != nil {
			return err
		}
	}
	return nil
}

type backlogCreateCommand struct {
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	Idea      string `json:"idea,omitempty"`
	Feature   string `json:"feature"`
}

type backlogStatusCommand struct {
	Status string `json:"status"`
}

type autoPickupPolicyView struct {
	Enabled          bool  `json:"enabled"`
	MaxParallelTasks int   `json:"max_parallel_tasks"`
	MaxTaskTokens    int64 `json:"max_task_tokens"`
	MaxDailyTokens   int64 `json:"max_daily_tokens"`
}

func (s *Server) handleListBacklog(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectFilter(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListBacklogItems()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "backlog is unavailable", "backlog_unavailable")
		return
	}
	if projectID != 0 {
		filtered := make([]store.BacklogItem, 0, len(items))
		for _, item := range items {
			if item.ProjectID == projectID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"items": items,
		"auto_pickup": autoPickupPolicyView{
			Enabled: s.autoPickup.Enabled, MaxParallelTasks: s.autoPickup.MaxParallelTasks,
			MaxTaskTokens: s.autoPickup.MaxTaskTokens, MaxDailyTokens: s.autoPickup.MaxDailyTokens,
		},
	})
}

func (s *Server) backlogActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if err := s.authorize(r, cloudidentity.PermissionStart, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return "", false
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	return actorID, true
}

func (s *Server) handleCreateBacklogItem(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.backlogActor(w, r)
	if !ok {
		return
	}
	var command backlogCreateCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if command.ProjectID <= 0 {
		writeProjectError(w, store.ErrProjectRequired)
		return
	}
	if !workflow.ValidFeature(command.Feature) || !store.ValidBacklogText(command.Title, maxBacklogTitleBytes) ||
		len(command.Idea) > maxBacklogIdeaBytes {
		writeJSONAPIError(w, http.StatusBadRequest, "backlog item requires a valid feature, a title and an idea within limits", "bad_backlog_item")
		return
	}
	item, err := s.store.CreateBacklogItem(command.ProjectID, strings.TrimSpace(command.Title), command.Idea, command.Feature, actorID, s.clock())
	if err != nil {
		if writeProjectError(w, err) {
			return
		}
		writeJSONAPIError(w, http.StatusInternalServerError, "backlog item could not be saved", "backlog_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusCreated, item)
}

func (s *Server) handleSetBacklogItemStatus(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.backlogActor(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "itemID"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid backlog item id", "bad_request")
		return
	}
	var command backlogStatusCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	switch command.Status {
	case autopickup.StatusDraft, autopickup.StatusReady, autopickup.StatusDiscarded:
	default:
		writeJSONAPIError(w, http.StatusBadRequest, "status must be draft, ready or discarded", "bad_backlog_status")
		return
	}
	item, err := s.store.SetBacklogItemStatus(id, command.Status, actorID, s.clock())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSONAPIError(w, http.StatusNotFound, "backlog item not found", "not_found")
	case errors.Is(err, store.ErrBacklogTransition):
		writeJSONAPIError(w, http.StatusConflict, err.Error(), "backlog_transition")
	case err != nil:
		writeJSONAPIError(w, http.StatusInternalServerError, "backlog item could not be updated", "backlog_unavailable")
	default:
		writeJSONResponse(w, http.StatusOK, item)
	}
}
