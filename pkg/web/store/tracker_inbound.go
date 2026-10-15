package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Предельные размеры недоверенных полей: значения приходят из трекера и
// попадают в журнал, UI и MCP, поэтому режутся и чистятся при сохранении.
const (
	trackerUntrustedMax = 256
	trackerReasonMax    = 512
)

// CleanTrackerText убирает управляющие и форматирующие символы (в том числе
// переводы строк и bidi-метки), схлопывает пробелы и обрезает результат до max
// байт по границе руны. Используется для всего, что пришло из трекера.
func CleanTrackerText(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// Входящие изменения статуса из трекера (S15). Каждое внешнее изменение
// получает ровно одну запись, ключ (tracker, change_id) — это идемпотентность:
// повторное чтение той же страницы курсора или повтор после сбоя не создаёт
// второй запрос. Запись хранит, что трекер просил (action/stage/to_stage), и
// честный итог: применено контроллером, ожидает условий, конфликт, проигнорировано.
// Локальное состояние задачи запись не меняет — это делает только контроллер.
const (
	// TrackerInboundReceived — изменение прочитано, итог ещё не записан
	// (сбой между чтением и решением); при следующем проходе оно решается заново.
	TrackerInboundReceived = "received"
	// TrackerInboundApplied — запрос выполнен штатным путём контроллера.
	TrackerInboundApplied = "applied"
	// TrackerInboundPending — условия не выполнены (гейты, подтверждение
	// человека, неподходящее состояние); запрос ждёт и показан оператору.
	TrackerInboundPending = "pending"
	// TrackerInboundConflict — запрос нельзя выполнить без решения оператора:
	// неоднозначная карта статусов или недопустимый маршрут.
	TrackerInboundConflict = "conflict"
	// TrackerInboundIgnored — не запрос: собственное эхо ai-team, статус вне
	// карты, карточка без связи.
	TrackerInboundIgnored = "ignored"
	// TrackerInboundSuperseded — граф ушёл с этапа запроса или связь снята.
	TrackerInboundSuperseded = "superseded"
	// TrackerInboundDismissed — оператор закрыл запрос явно.
	TrackerInboundDismissed = "dismissed"
)

var (
	// ErrInboundNotFound — записи входящего изменения нет.
	ErrInboundNotFound = errors.New("tracker inbound change not found")
	// ErrInboundNotOpen — запрос уже выполнен, закрыт или заменён.
	ErrInboundNotOpen = errors.New("tracker inbound request is not open")
)

// TrackerInbound — одно входящее изменение и его судьба.
type TrackerInbound struct {
	Tracker    string `json:"tracker"`
	ChangeID   string `json:"change_id"`
	TaskID     int64  `json:"task_id"`
	IssueKey   string `json:"issue_key"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	// Origin — ai-team (собственное эхо) или external.
	Origin    string    `json:"origin"`
	Author    string    `json:"author,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
	State     string    `json:"state"`
	// Action, Stage и ToStage — разобранный запрос по карте tracker.inbound.
	Action  string `json:"action,omitempty"`
	Stage   string `json:"stage,omitempty"`
	ToStage string `json:"to_stage,omitempty"`
	// Reason — почему запрос не выполнен или проигнорирован.
	Reason string `json:"reason,omitempty"`
	// SuggestedAction — допустимое действие оператора по штатному маршруту.
	SuggestedAction string    `json:"suggested_action,omitempty"`
	ResolvedBy      string    `json:"resolved_by,omitempty"`
	DetectedAt      time.Time `json:"detected_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TrackerSyncState — состояние опроса трекера: курсор и здоровье.
type TrackerSyncState struct {
	Tracker             string     `json:"tracker"`
	Cursor              string     `json:"cursor"`
	LastPollAt          *time.Time `json:"last_poll_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	LastErrorAt         *time.Time `json:"last_error_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastChanges         int        `json:"last_changes"`
}

// migrateV18 (S15) добавляет журнал входящих изменений и состояние опроса
// трекера. Отметку версии 17 ставит migrateV17 (S06).
func migrateV18(ctx context.Context, tx schemaQuerier) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS tracker_inbound_changes (
			tracker TEXT NOT NULL, change_id TEXT NOT NULL, task_id INTEGER NOT NULL DEFAULT 0, issue_key TEXT NOT NULL,
			from_status TEXT NOT NULL DEFAULT '', to_status TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT '', changed_at DATETIME NOT NULL, state TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT '', stage TEXT NOT NULL DEFAULT '', to_stage TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '', suggested_action TEXT NOT NULL DEFAULT '', resolved_by TEXT NOT NULL DEFAULT '',
			detected_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
			PRIMARY KEY(tracker, change_id))`,
		`CREATE INDEX IF NOT EXISTS idx_tracker_inbound_task ON tracker_inbound_changes(task_id, detected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tracker_inbound_state ON tracker_inbound_changes(state, detected_at)`,
		`CREATE TABLE IF NOT EXISTS tracker_sync_state (
			tracker TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT '', last_poll_at DATETIME, last_success_at DATETIME,
			last_error TEXT NOT NULL DEFAULT '', last_error_at DATETIME, consecutive_failures INTEGER NOT NULL DEFAULT 0,
			last_changes INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (18, CURRENT_TIMESTAMP)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// GetTrackerSyncState возвращает состояние опроса или (nil, nil).
func (s *Store) GetTrackerSyncState(tracker string) (*TrackerSyncState, error) {
	var state TrackerSyncState
	var poll, success, failed sql.NullTime
	err := s.db.QueryRow(`SELECT tracker, cursor, last_poll_at, last_success_at, last_error, last_error_at, consecutive_failures, last_changes
		FROM tracker_sync_state WHERE tracker = ?`, tracker).
		Scan(&state.Tracker, &state.Cursor, &poll, &success, &state.LastError, &failed, &state.ConsecutiveFailures, &state.LastChanges)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.LastPollAt, state.LastSuccessAt, state.LastErrorAt = utcPtr(poll), utcPtr(success), utcPtr(failed)
	return &state, nil
}

func utcPtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

// SaveTrackerSyncCursor запоминает курсор после успешной обработки страницы.
func (s *Store) SaveTrackerSyncCursor(tracker, cursor string, changes int, at time.Time) error {
	at = at.UTC()
	_, err := s.db.Exec(`INSERT INTO tracker_sync_state (tracker, cursor, last_poll_at, last_success_at, last_changes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tracker) DO UPDATE SET cursor = excluded.cursor, last_poll_at = excluded.last_poll_at,
			last_success_at = excluded.last_success_at, last_error = '', last_error_at = NULL,
			consecutive_failures = 0, last_changes = excluded.last_changes`, tracker, cursor, at, at, changes)
	return err
}

// RecordTrackerSyncFailure записывает сбой опроса, не трогая курсор: следующий
// проход читает с того же места, поэтому изменения не теряются и не дублируются.
func (s *Store) RecordTrackerSyncFailure(tracker, message string, at time.Time) error {
	at = at.UTC()
	_, err := s.db.Exec(`INSERT INTO tracker_sync_state (tracker, cursor, last_poll_at, last_error, last_error_at, consecutive_failures)
		VALUES (?, '', ?, ?, ?, 1)
		ON CONFLICT(tracker) DO UPDATE SET last_poll_at = excluded.last_poll_at, last_error = excluded.last_error,
			last_error_at = excluded.last_error_at, consecutive_failures = consecutive_failures + 1`, tracker, at, message, at)
	return err
}

// BeginTrackerInbound создаёт запись об изменении в состоянии received. Если
// запись уже есть, возвращает её и created=false (идемпотентность по ID
// внешнего изменения).
func (s *Store) BeginTrackerInbound(record TrackerInbound, at time.Time) (TrackerInbound, bool, error) {
	at = at.UTC()
	record.State = TrackerInboundReceived
	record.ChangeID = CleanTrackerText(record.ChangeID, trackerUntrustedMax)
	record.FromStatus = CleanTrackerText(record.FromStatus, trackerUntrustedMax)
	record.ToStatus = CleanTrackerText(record.ToStatus, trackerUntrustedMax)
	record.Author = CleanTrackerText(record.Author, trackerUntrustedMax)
	record.ChangedAt = record.ChangedAt.UTC()
	result, err := s.db.Exec(`INSERT OR IGNORE INTO tracker_inbound_changes
		(tracker, change_id, task_id, issue_key, from_status, to_status, origin, author, changed_at, state, detected_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Tracker, record.ChangeID, record.TaskID, record.IssueKey, record.FromStatus, record.ToStatus,
		record.Origin, record.Author, record.ChangedAt, record.State, at, at)
	if err != nil {
		return TrackerInbound{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return TrackerInbound{}, false, err
	}
	stored, err := s.GetTrackerInbound(record.Tracker, record.ChangeID)
	return stored, affected > 0, err
}

const trackerInboundColumns = `tracker, change_id, task_id, issue_key, from_status, to_status, origin, author, changed_at, state,
	action, stage, to_stage, reason, suggested_action, resolved_by, detected_at, updated_at`

func scanTrackerInbound(scan func(...any) error) (TrackerInbound, error) {
	var item TrackerInbound
	err := scan(&item.Tracker, &item.ChangeID, &item.TaskID, &item.IssueKey, &item.FromStatus, &item.ToStatus, &item.Origin,
		&item.Author, &item.ChangedAt, &item.State, &item.Action, &item.Stage, &item.ToStage, &item.Reason,
		&item.SuggestedAction, &item.ResolvedBy, &item.DetectedAt, &item.UpdatedAt)
	item.ChangedAt, item.DetectedAt, item.UpdatedAt = item.ChangedAt.UTC(), item.DetectedAt.UTC(), item.UpdatedAt.UTC()
	return item, err
}

// GetTrackerInbound возвращает запись по ключу изменения.
func (s *Store) GetTrackerInbound(tracker, changeID string) (TrackerInbound, error) {
	item, err := scanTrackerInbound(s.db.QueryRow(`SELECT `+trackerInboundColumns+` FROM tracker_inbound_changes WHERE tracker = ? AND change_id = ?`, tracker, changeID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackerInbound{}, ErrInboundNotFound
	}
	return item, err
}

// ResolveTrackerInbound записывает итог. Закрытое оператором или заменённое
// изменение не оживает: запись в dismissed/superseded не перезаписывается.
func (s *Store) ResolveTrackerInbound(record TrackerInbound, at time.Time) (TrackerInbound, error) {
	record.ChangeID = CleanTrackerText(record.ChangeID, trackerUntrustedMax)
	record.Reason = CleanTrackerText(record.Reason, trackerReasonMax)
	record.ResolvedBy = CleanTrackerText(record.ResolvedBy, trackerUntrustedMax)
	_, err := s.db.Exec(`UPDATE tracker_inbound_changes SET state = ?, action = ?, stage = ?, to_stage = ?, reason = ?,
		suggested_action = ?, resolved_by = ?, task_id = ?, updated_at = ?
		WHERE tracker = ? AND change_id = ? AND state NOT IN (?, ?)`,
		record.State, record.Action, record.Stage, record.ToStage, record.Reason, record.SuggestedAction,
		record.ResolvedBy, record.TaskID, at.UTC(), record.Tracker, record.ChangeID,
		TrackerInboundDismissed, TrackerInboundSuperseded)
	if err != nil {
		return TrackerInbound{}, err
	}
	return s.GetTrackerInbound(record.Tracker, record.ChangeID)
}

// ListTrackerInbound возвращает записи, новые первыми. taskID > 0 ограничивает
// задачей; states — набор состояний (пусто — все).
func (s *Store) ListTrackerInbound(taskID int64, states []string, limit int) ([]TrackerInbound, error) {
	query, args := `SELECT `+trackerInboundColumns+` FROM tracker_inbound_changes WHERE 1 = 1`, []any{}
	if taskID > 0 {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	if len(states) > 0 {
		query += ` AND state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, state := range states {
			args = append(args, state)
		}
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(query+` ORDER BY detected_at DESC, change_id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // ошибки чтения возвращает rows.Err ниже.
	items := []TrackerInbound{}
	for rows.Next() {
		item, err := scanTrackerInbound(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DismissTrackerInbound закрывает ожидающий запрос или конфликт явным
// решением оператора. Возвращает ErrInboundNotOpen, если запрос уже не открыт.
func (s *Store) DismissTrackerInbound(tracker, changeID, actorID, reason string, at time.Time) (TrackerInbound, error) {
	result, err := s.db.Exec(`UPDATE tracker_inbound_changes SET state = ?, resolved_by = ?, reason = ?, updated_at = ?
		WHERE tracker = ? AND change_id = ? AND state IN (?, ?)`,
		TrackerInboundDismissed, actorID, reason, at.UTC(), tracker, changeID, TrackerInboundPending, TrackerInboundConflict)
	if err != nil {
		return TrackerInbound{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		if _, getErr := s.GetTrackerInbound(tracker, changeID); getErr != nil {
			return TrackerInbound{}, getErr
		}
		return TrackerInbound{}, ErrInboundNotOpen
	}
	return s.GetTrackerInbound(tracker, changeID)
}

// SupersedeTrackerInbound помечает открытый запрос заменённым с причиной.
func (s *Store) SupersedeTrackerInbound(tracker, changeID, reason string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE tracker_inbound_changes SET state = ?, reason = ?, updated_at = ?
		WHERE tracker = ? AND change_id = ? AND state IN (?, ?)`,
		TrackerInboundSuperseded, reason, at.UTC(), tracker, changeID, TrackerInboundPending, TrackerInboundConflict)
	return err
}
