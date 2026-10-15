package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Задача связана с внешней карточкой трекера явной записью tracker_links, а
// не ссылкой в произвольном массиве external_links. Связь строго 1:1: PRIMARY
// KEY(task_id) не даёт задаче вторую карточку, UNIQUE(tracker, issue_key) не
// даёт карточке вторую задачу. Нарушение исправляется только явно.
var (
	// ErrTaskAlreadyLinked — у задачи уже другая карточка; сначала снимите связь.
	ErrTaskAlreadyLinked = errors.New("task is already linked to another tracker issue")
	// ErrIssueAlreadyLinked — карточка уже связана с другой задачей.
	ErrIssueAlreadyLinked = errors.New("tracker issue is already linked to another task")
	// ErrDeliveryNotFound — записи доставки нет.
	ErrDeliveryNotFound = errors.New("tracker delivery not found")
)

// Статусы доставки в трекер.
const (
	TrackerDeliveryDelivered = "delivered"
	TrackerDeliveryFailed    = "failed"
)

// TrackerLink — связь задачи (pipeline_runs.id) с карточкой.
type TrackerLink struct {
	TaskID   int64     `json:"task_id"`
	Tracker  string    `json:"tracker"`
	IssueKey string    `json:"issue_key"`
	LinkedBy string    `json:"linked_by,omitempty"`
	LinkedAt time.Time `json:"linked_at"`
}

// TrackerLinkConflict — неоднозначная старая ссылка, которую миграция не
// угадывала. Остаётся видимой, пока оператор явно не свяжет или не снимет связь.
type TrackerLinkConflict struct {
	TaskID     int64     `json:"task_id"`
	IssueKey   string    `json:"issue_key"`
	Reason     string    `json:"reason"`
	DetectedAt time.Time `json:"detected_at"`
}

// TrackerDelivery — долговечная запись об исходящей доставке одного события.
// Ошибка трекера не откатывает прогон, но остаётся здесь, пока её не повторят.
type TrackerDelivery struct {
	EventID     int64      `json:"event_id"`
	TaskID      int64      `json:"task_id"`
	Tracker     string     `json:"tracker"`
	IssueKey    string     `json:"issue_key"`
	EventType   string     `json:"event_type"`
	Stage       string     `json:"stage,omitempty"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// migrateV16 (S14) добавляет явную связь задачи с карточкой трекера, журнал
// конфликтов миграции ссылок и журнал исходящих доставок. Схема не знает про
// конфигурацию трекера, поэтому сами ссылки переносит ImportTrackerLinks при
// старте сервера с настроенным трекером.
func migrateV16(ctx context.Context, tx schemaQuerier) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS tracker_links (
			task_id INTEGER PRIMARY KEY, tracker TEXT NOT NULL, issue_key TEXT NOT NULL,
			linked_by TEXT NOT NULL DEFAULT '', linked_at DATETIME NOT NULL,
			UNIQUE(tracker, issue_key))`,
		`CREATE TABLE IF NOT EXISTS tracker_link_conflicts (
			task_id INTEGER NOT NULL, issue_key TEXT NOT NULL, reason TEXT NOT NULL, detected_at DATETIME NOT NULL,
			PRIMARY KEY(task_id, issue_key))`,
		`CREATE TABLE IF NOT EXISTS tracker_link_imports (
			tracker TEXT PRIMARY KEY, imported_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS tracker_deliveries (
			event_id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, tracker TEXT NOT NULL, issue_key TEXT NOT NULL,
			event_type TEXT NOT NULL, stage TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, delivered_at DATETIME)`,
		`CREATE INDEX IF NOT EXISTS idx_tracker_deliveries_task ON tracker_deliveries(task_id, event_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracker_deliveries_status ON tracker_deliveries(status, event_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// GetTrackerLink возвращает связь задачи или (nil, nil).
func (s *Store) GetTrackerLink(taskID int64) (*TrackerLink, error) {
	var link TrackerLink
	err := s.db.QueryRow(`SELECT task_id, tracker, issue_key, linked_by, linked_at FROM tracker_links WHERE task_id = ?`, taskID).
		Scan(&link.TaskID, &link.Tracker, &link.IssueKey, &link.LinkedBy, &link.LinkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	link.LinkedAt = link.LinkedAt.UTC()
	return &link, nil
}

// TrackerLinkByIssue возвращает связь, которой принадлежит карточка, или (nil, nil).
func (s *Store) TrackerLinkByIssue(tracker, issueKey string) (*TrackerLink, error) {
	var link TrackerLink
	err := s.db.QueryRow(`SELECT task_id, tracker, issue_key, linked_by, linked_at FROM tracker_links WHERE tracker = ? AND issue_key = ?`, tracker, issueKey).
		Scan(&link.TaskID, &link.Tracker, &link.IssueKey, &link.LinkedBy, &link.LinkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	link.LinkedAt = link.LinkedAt.UTC()
	return &link, nil
}

// ListTrackerLinks возвращает все связи (для опроса входящих изменений).
func (s *Store) ListTrackerLinks() ([]TrackerLink, error) {
	rows, err := s.db.Query(`SELECT task_id, tracker, issue_key, linked_by, linked_at FROM tracker_links ORDER BY task_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // ошибки чтения возвращает rows.Err ниже.
	var links []TrackerLink
	for rows.Next() {
		var link TrackerLink
		if err := rows.Scan(&link.TaskID, &link.Tracker, &link.IssueKey, &link.LinkedBy, &link.LinkedAt); err != nil {
			return nil, err
		}
		link.LinkedAt = link.LinkedAt.UTC()
		links = append(links, link)
	}
	return links, rows.Err()
}

// SetTrackerLink явно связывает задачу с карточкой. Повтор той же пары
// идемпотентен. Другая карточка у задачи или другая задача у карточки —
// ErrTaskAlreadyLinked / ErrIssueAlreadyLinked: связь не перезаписывается
// молча. Успешная явная связь снимает записанные конфликты этой задачи.
func (s *Store) SetTrackerLink(taskID int64, tracker, issueKey, actorID string, at time.Time) (TrackerLink, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerLink{}, err
	}
	defer func() { _ = tx.Rollback() }() // после Commit откат — no-op; значимые ошибки возвращены выше.
	link, err := insertTrackerLink(tx, taskID, tracker, issueKey, actorID, at.UTC())
	if err != nil {
		return TrackerLink{}, err
	}
	if _, err := tx.Exec(`DELETE FROM tracker_link_conflicts WHERE task_id = ?`, taskID); err != nil {
		return TrackerLink{}, err
	}
	return link, tx.Commit()
}

type execQuerier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func insertTrackerLink(q execQuerier, taskID int64, tracker, issueKey, actorID string, at time.Time) (TrackerLink, error) {
	var existingTracker, existingKey string
	err := q.QueryRow(`SELECT tracker, issue_key FROM tracker_links WHERE task_id = ?`, taskID).Scan(&existingTracker, &existingKey)
	switch {
	case err == nil:
		if existingTracker == tracker && existingKey == issueKey {
			var link TrackerLink
			err := q.QueryRow(`SELECT task_id, tracker, issue_key, linked_by, linked_at FROM tracker_links WHERE task_id = ?`, taskID).
				Scan(&link.TaskID, &link.Tracker, &link.IssueKey, &link.LinkedBy, &link.LinkedAt)
			link.LinkedAt = link.LinkedAt.UTC()
			return link, err
		}
		return TrackerLink{}, ErrTaskAlreadyLinked
	case !errors.Is(err, sql.ErrNoRows):
		return TrackerLink{}, err
	}
	var owner int64
	switch err := q.QueryRow(`SELECT task_id FROM tracker_links WHERE tracker = ? AND issue_key = ?`, tracker, issueKey).Scan(&owner); {
	case err == nil:
		return TrackerLink{}, ErrIssueAlreadyLinked
	case !errors.Is(err, sql.ErrNoRows):
		return TrackerLink{}, err
	}
	if _, err := q.Exec(`INSERT INTO tracker_links (task_id, tracker, issue_key, linked_by, linked_at) VALUES (?, ?, ?, ?, ?)`,
		taskID, tracker, issueKey, actorID, at); err != nil {
		if isUniqueViolation(err) {
			return TrackerLink{}, ErrIssueAlreadyLinked
		}
		return TrackerLink{}, err
	}
	return TrackerLink{TaskID: taskID, Tracker: tracker, IssueKey: issueKey, LinkedBy: actorID, LinkedAt: at}, nil
}

// DeleteTrackerLink снимает связь задачи (явное исправление) вместе с её
// конфликтами. Возвращает false, если связи не было.
func (s *Store) DeleteTrackerLink(taskID int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }() // после Commit откат — no-op; значимые ошибки возвращены выше.
	result, err := tx.Exec(`DELETE FROM tracker_links WHERE task_id = ?`, taskID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM tracker_link_conflicts WHERE task_id = ?`, taskID); err != nil {
		return false, err
	}
	return affected > 0, tx.Commit()
}

// ListTrackerLinkConflicts возвращает конфликты одной задачи (taskID > 0) или все.
func (s *Store) ListTrackerLinkConflicts(taskID int64) ([]TrackerLinkConflict, error) {
	query, args := `SELECT task_id, issue_key, reason, detected_at FROM tracker_link_conflicts`, []any{}
	if taskID > 0 {
		query += ` WHERE task_id = ?`
		args = append(args, taskID)
	}
	rows, err := s.db.Query(query+` ORDER BY task_id, issue_key`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // ошибки чтения возвращает rows.Err ниже.
	conflicts := []TrackerLinkConflict{}
	for rows.Next() {
		var conflict TrackerLinkConflict
		if err := rows.Scan(&conflict.TaskID, &conflict.IssueKey, &conflict.Reason, &conflict.DetectedAt); err != nil {
			return nil, err
		}
		conflict.DetectedAt = conflict.DetectedAt.UTC()
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

// TrackerLinkImportResult — итог переноса старых ссылок.
type TrackerLinkImportResult struct {
	Skipped   bool `json:"skipped"`
	Linked    int  `json:"linked"`
	Conflicts int  `json:"conflicts"`
}

// ImportTrackerLinks один раз для трекера переносит ключи из старых
// external_links в tracker_links. extract возвращает ключи, которые трекер
// распознал в снимке задачи. Переносится только однозначное: у задачи ровно
// один ключ и ни одна другая задача на него не ссылается. Всё остальное
// записывается как конфликт и не угадывается. Повторный запуск для того же
// трекера ничего не делает, чтобы не воскрешать исправленные оператором данные.
func (s *Store) ImportTrackerLinks(tracker string, extract func(snapshot string) []string, at time.Time) (TrackerLinkImportResult, error) {
	at = at.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerLinkImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }() // после Commit откат — no-op; значимые ошибки возвращены выше.
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tracker_link_imports WHERE tracker = ?`, tracker).Scan(&done); err != nil {
		return TrackerLinkImportResult{}, err
	}
	if done > 0 {
		return TrackerLinkImportResult{Skipped: true}, nil
	}
	rows, err := tx.Query(`SELECT id, COALESCE(config_snapshot, '') FROM pipeline_runs ORDER BY id`)
	if err != nil {
		return TrackerLinkImportResult{}, err
	}
	type candidate struct {
		task int64
		keys []string
	}
	var candidates []candidate
	for rows.Next() {
		var id int64
		var snapshot string
		if err := rows.Scan(&id, &snapshot); err != nil {
			_ = rows.Close()
			return TrackerLinkImportResult{}, err
		}
		if keys := distinctSorted(extract(snapshot)); len(keys) > 0 {
			candidates = append(candidates, candidate{task: id, keys: keys})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return TrackerLinkImportResult{}, err
	}
	_ = rows.Close()
	owners := map[string][]int64{}
	for _, item := range candidates {
		for _, key := range item.keys {
			owners[key] = append(owners[key], item.task)
		}
	}
	var result TrackerLinkImportResult
	conflict := func(task int64, key, reason string) error {
		result.Conflicts++
		_, err := tx.Exec(`INSERT OR IGNORE INTO tracker_link_conflicts (task_id, issue_key, reason, detected_at) VALUES (?, ?, ?, ?)`, task, key, reason, at)
		return err
	}
	for _, item := range candidates {
		if len(item.keys) > 1 {
			for _, key := range item.keys {
				if err := conflict(item.task, key, "task references several tracker issues: "+strings.Join(item.keys, ", ")); err != nil {
					return TrackerLinkImportResult{}, err
				}
			}
			continue
		}
		key := item.keys[0]
		if len(owners[key]) > 1 {
			if err := conflict(item.task, key, fmt.Sprintf("issue is referenced by %d tasks", len(owners[key]))); err != nil {
				return TrackerLinkImportResult{}, err
			}
			continue
		}
		_, err := insertTrackerLink(tx, item.task, tracker, key, "migration", at)
		switch {
		case err == nil:
			result.Linked++
		case errors.Is(err, ErrIssueAlreadyLinked), errors.Is(err, ErrTaskAlreadyLinked):
			if err := conflict(item.task, key, "link already exists for another task or issue"); err != nil {
				return TrackerLinkImportResult{}, err
			}
		default:
			return TrackerLinkImportResult{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO tracker_link_imports (tracker, imported_at) VALUES (?, ?)`, tracker, at); err != nil {
		return TrackerLinkImportResult{}, err
	}
	return result, tx.Commit()
}

func distinctSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// RecordTrackerDelivery сохраняет итог попытки доставки события. Каждая
// запись увеличивает attempts; успех очищает ошибку, неудача сохраняет её.
func (s *Store) RecordTrackerDelivery(d TrackerDelivery, at time.Time) (TrackerDelivery, error) {
	at = at.UTC()
	var delivered any
	if d.Status == TrackerDeliveryDelivered {
		delivered = at
	}
	_, err := s.db.Exec(`INSERT INTO tracker_deliveries
		(event_id, task_id, tracker, issue_key, event_type, stage, status, attempts, last_error, created_at, updated_at, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET status = excluded.status, attempts = tracker_deliveries.attempts + 1,
			last_error = excluded.last_error, updated_at = excluded.updated_at,
			delivered_at = excluded.delivered_at, issue_key = excluded.issue_key, tracker = excluded.tracker`,
		d.EventID, d.TaskID, d.Tracker, d.IssueKey, d.EventType, d.Stage, d.Status, d.LastError, at, at, delivered)
	if err != nil {
		return TrackerDelivery{}, err
	}
	return s.GetTrackerDelivery(d.EventID)
}

const trackerDeliveryColumns = `event_id, task_id, tracker, issue_key, event_type, stage, status, attempts, last_error, created_at, updated_at, delivered_at`

func scanTrackerDelivery(scanner interface{ Scan(...any) error }) (TrackerDelivery, error) {
	var d TrackerDelivery
	var delivered sql.NullTime
	if err := scanner.Scan(&d.EventID, &d.TaskID, &d.Tracker, &d.IssueKey, &d.EventType, &d.Stage, &d.Status,
		&d.Attempts, &d.LastError, &d.CreatedAt, &d.UpdatedAt, &delivered); err != nil {
		return TrackerDelivery{}, err
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	if delivered.Valid {
		value := delivered.Time.UTC()
		d.DeliveredAt = &value
	}
	return d, nil
}

// GetTrackerDelivery возвращает запись доставки события.
func (s *Store) GetTrackerDelivery(eventID int64) (TrackerDelivery, error) {
	d, err := scanTrackerDelivery(s.db.QueryRow(`SELECT `+trackerDeliveryColumns+` FROM tracker_deliveries WHERE event_id = ?`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return TrackerDelivery{}, ErrDeliveryNotFound
	}
	return d, err
}

// ListTrackerDeliveries возвращает доставки задачи (taskID > 0) или всех
// задач, новые первыми. status пустой — любые.
func (s *Store) ListTrackerDeliveries(taskID int64, status string, limit int) ([]TrackerDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + trackerDeliveryColumns + ` FROM tracker_deliveries WHERE 1 = 1`
	var args []any
	if taskID > 0 {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.db.Query(query+` ORDER BY event_id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // ошибки чтения возвращает rows.Err ниже.
	deliveries := []TrackerDelivery{}
	for rows.Next() {
		d, err := scanTrackerDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// GetEventByID возвращает событие по глобальному ID или (nil, nil).
func (s *Store) GetEventByID(id int64) (*Event, error) {
	var event Event
	err := s.db.QueryRow(`SELECT id, run_uid, sequence, type, COALESCE(stage, ''), COALESCE(actor, ''), COALESCE(executor, ''), COALESCE(severity, ''), COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')
		FROM events WHERE id = ?`, id).
		Scan(&event.ID, &event.RunID, &event.Sequence, &event.Type, &event.Stage, &event.Actor, &event.Executor, &event.Severity, &event.AttemptID, &event.Timestamp, &event.DataJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}
