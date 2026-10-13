package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 13

type PipelineRun struct {
	ID             int64      `json:"id"`
	RunID          string     `json:"run_id"`
	Feature        string     `json:"feature"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ConfigSnapshot string     `json:"config_snapshot,omitempty"`
	QueueJobID     int64      `json:"queue_job_id,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type Stage struct {
	ID            int64      `json:"id"`
	PipelineRunID int64      `json:"pipeline_run_id"`
	AttemptID     string     `json:"attempt_id"`
	StageIndex    int        `json:"stage_index"`
	AgentName     string     `json:"agent_name"`
	Status        string     `json:"status"`
	Execution     string     `json:"execution,omitempty"`
	Decision      string     `json:"decision,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DurationMs    int64      `json:"duration_ms"`
	Error         string     `json:"error,omitempty"`
	Verdict       string     `json:"verdict,omitempty"`
	InputsJSON    string     `json:"inputs_json,omitempty"`
	OutputsJSON   string     `json:"outputs_json,omitempty"`
	ChecksJSON    string     `json:"checks_json,omitempty"`
	MutationsJSON string     `json:"mutations_json,omitempty"`
	DeliveryJSON  string     `json:"delivery_json,omitempty"`
}

type Event struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Sequence  int64     `json:"sequence"`
	Type      string    `json:"type"`
	Stage     string    `json:"stage,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Executor  string    `json:"executor,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	AttemptID string    `json:"attempt_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	DataJSON  string    `json:"data_json,omitempty"`
	DedupeKey string    `json:"-"`
}

type Store struct {
	db       *sql.DB
	eventBus *notifier.EventBus
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// A single pooled connection makes PRAGMA settings deterministic and still
	// allows cross-process readers/writers through WAL + busy_timeout.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if dbPath != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, eventBus: notifier.NewEventBus()}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SubscribeEvents returns a live subscription for events committed through
// this store. Consumers that reconnect should use the SQLite cursor API to
// replay any events missed while disconnected.
func (s *Store) SubscribeEvents(ctx context.Context, capacity int) <-chan notifier.Event {
	return s.eventBus.Subscribe(ctx, capacity)
}

func (s *Store) CreatePipelineRun(run *PipelineRun) error {
	result, err := s.db.Exec(
		"INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot) VALUES (?, ?, ?, ?, ?)",
		run.RunID, run.Feature, run.Status, run.StartedAt, run.ConfigSnapshot,
	)
	if err != nil {
		return err
	}
	run.ID, err = result.LastInsertId()
	return err
}

// AdmitPipelineRun makes an accepted command visible before a worker claims it.
func (s *Store) AdmitPipelineRun(run *PipelineRun) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot)
		VALUES (?, ?, 'queued', ?, ?) ON CONFLICT DO NOTHING`, run.RunID, run.Feature, run.StartedAt, run.ConfigSnapshot)
	if err != nil {
		return err
	}
	stored, err := s.scanRun(tx.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at,
		completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs WHERE run_uid = ?`, run.RunID))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = *stored
	return nil
}

// MarkRunQueued correlates the durable queue identity with the dashboard row.
func (s *Store) MarkRunQueued(runID string, queueJobID int64) error {
	return s.MarkRunQueuedStatus(runID, queueJobID, "pending", "", time.Now().UTC())
}

func (s *Store) MarkRunQueuedStatus(runID string, queueJobID int64, status string, cause string, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE pipeline_runs SET queue_job_id = ?,
		status = CASE
			WHEN status = 'queued' AND ? IN ('running', 'failed', 'canceled') THEN ?
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued') THEN 'queued'
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? = 'running' THEN 'running'
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('failed', 'canceled') THEN ?
			ELSE status END,
		completed_at = CASE
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued', 'running') THEN NULL
			WHEN ? IN ('failed', 'canceled') THEN ? ELSE completed_at END,
		admission_error = CASE
			WHEN status = 'failed' AND COALESCE(queue_job_id, 0) = 0 AND ? IN ('pending', 'queued', 'running') THEN ''
			WHEN ? IN ('failed', 'canceled') THEN ?
			WHEN status = 'queued' AND ? IN ('pending', 'queued', 'running') THEN ''
			ELSE admission_error END
		WHERE run_uid = ?`, queueJobID,
		status, status, status, status, status, status,
		status, status, at,
		status, status, cause, status, runID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "pipeline run queue identity"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkQueuedCanceled(runID string, at time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'canceled', completed_at = ?
		WHERE run_uid = ? AND status = 'queued'`, at, runID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) UpdateQueueProjection(queueJobID int64, status, cause string, at time.Time) (string, bool, error) {
	var runID string
	var current string
	err := s.db.QueryRow(`SELECT COALESCE(run_uid, ''), status FROM pipeline_runs WHERE queue_job_id = ?`, queueJobID).Scan(&runID, &current)
	if err != nil {
		return "", false, err
	}
	if current == status {
		return runID, false, nil
	}
	var result sql.Result
	if status == "failed" || status == "canceled" {
		result, err = s.db.Exec(`UPDATE pipeline_runs SET status = ?, completed_at = ?, admission_error = ?
			WHERE queue_job_id = ? AND status IN ('queued', 'running')`, status, at, cause, queueJobID)
	} else if status == "running" {
		result, err = s.db.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL, admission_error = '' WHERE queue_job_id = ? AND status = 'queued'`, queueJobID)
	}
	if err != nil {
		return runID, false, err
	}
	if result == nil {
		return runID, false, nil
	}
	affected, err := result.RowsAffected()
	return runID, affected > 0, err
}

func (s *Store) QueuedRunIDs() ([]struct {
	RunID      string
	QueueJobID int64
}, error) {
	rows, err := s.db.Query(`SELECT run_uid, queue_job_id FROM pipeline_runs WHERE queue_job_id > 0 AND status IN ('queued', 'running')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var values []struct {
		RunID      string
		QueueJobID int64
	}
	for rows.Next() {
		var value struct {
			RunID      string
			QueueJobID int64
		}
		if err := rows.Scan(&value.RunID, &value.QueueJobID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// PendingAdmissions are visible start requests whose queue identity has not
// yet been correlated. The task is stored with the admission so startup can
// safely recreate a missing scheduler job after a process crash.
func (s *Store) PendingAdmissions() ([]PipelineRun, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at,
		completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs WHERE status IN ('queued', 'failed') AND COALESCE(queue_job_id, 0) = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt,
			&run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) MarkRunAdmissionFailure(runID, cause string, at time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'failed', completed_at = ?, admission_error = ?
		WHERE run_uid = ? AND COALESCE(queue_job_id, 0) = 0 AND status IN ('queued', 'running')`, at, cause, runID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// StartAdmittedRun changes the pre-created queue projection to running, or
// inserts a normal local run if no admission projection exists.
func (s *Store) StartAdmittedRun(run *PipelineRun) error {
	result, err := s.db.Exec(`INSERT INTO pipeline_runs (run_uid, feature, status, started_at, config_snapshot)
		VALUES (?, ?, 'running', ?, ?) ON CONFLICT DO UPDATE SET
		feature = excluded.feature, status = 'running', started_at = excluded.started_at,
		completed_at = NULL, config_snapshot = excluded.config_snapshot, admission_error = ''`,
		run.RunID, run.Feature, run.StartedAt, run.ConfigSnapshot)
	if err != nil {
		return err
	}
	stored, err := s.GetPipelineRunByRunID(run.RunID)
	if err != nil {
		return err
	}
	run.ID = stored.ID
	_, err = result.RowsAffected()
	return err
}

func (s *Store) UpdatePipelineRun(run *PipelineRun) error {
	result, err := s.db.Exec(
		"UPDATE pipeline_runs SET status = ?, completed_at = ? WHERE id = ? AND (? = '' OR run_uid = ?)",
		run.Status, run.CompletedAt, run.ID, run.RunID, run.RunID,
	)
	if err != nil {
		return err
	}
	return requireAffected(result, "pipeline run")
}

func (s *Store) ResumePipelineRun(runID string) (*PipelineRun, error) {
	run, err := s.GetPipelineRunByRunID(runID)
	if err != nil {
		return nil, err
	}
	result, err := s.db.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL WHERE id = ? AND run_uid = ?`, run.ID, runID)
	if err != nil {
		return nil, err
	}
	if err := requireAffected(result, "resumed pipeline run"); err != nil {
		return nil, err
	}
	run.Status, run.CompletedAt = "running", nil
	return run, nil
}

func (s *Store) LatestRunEventSequence(runID string) (int64, error) {
	var sequence int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM events WHERE run_uid = ?`, runID).Scan(&sequence)
	return sequence, err
}

func (s *Store) CreateStage(stage *Stage) error {
	result, err := s.db.Exec(`INSERT INTO stages
		(pipeline_run_id, attempt_uid, stage_index, agent_name, status, execution, decision, outcome, started_at, error, verdict, inputs_json, outputs_json, checks_json, mutations_json, delivery_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stage.PipelineRunID, stage.AttemptID, stage.StageIndex, stage.AgentName, stage.Status,
		stage.Execution, stage.Decision, stage.Outcome, stage.StartedAt, stage.Error, stage.Verdict,
		stage.InputsJSON, stage.OutputsJSON, stage.ChecksJSON, stage.MutationsJSON, stage.DeliveryJSON,
	)
	if err != nil {
		return err
	}
	stage.ID, err = result.LastInsertId()
	return err
}

func (s *Store) UpdateStage(stage *Stage) error {
	result, err := s.db.Exec(`UPDATE stages SET status = ?, execution = ?, decision = ?, outcome = ?, completed_at = ?, duration_ms = ?,
		error = ?, verdict = ?, inputs_json = ?, outputs_json = ?, checks_json = ?, mutations_json = ?, delivery_json = ?
		WHERE id = ? AND (? = '' OR attempt_uid = ?)`,
		stage.Status, stage.Execution, stage.Decision, stage.Outcome, stage.CompletedAt, stage.DurationMs,
		stage.Error, stage.Verdict, stage.InputsJSON, stage.OutputsJSON, stage.ChecksJSON, stage.MutationsJSON, stage.DeliveryJSON,
		stage.ID, stage.AttemptID, stage.AttemptID,
	)
	if err != nil {
		return err
	}
	return requireAffected(result, "stage")
}

func (s *Store) InvalidateAttempts(pipelineRunID int64, attemptIDs []string, at time.Time) error {
	transaction, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }() // Rollback после успешного Commit возвращает ErrTxDone — штатный исход этого defer.
	for _, attemptID := range attemptIDs {
		result, updateErr := transaction.Exec(`UPDATE stages SET status = 'invalidated', outcome = 'invalidated', completed_at = COALESCE(completed_at, ?)
			WHERE pipeline_run_id = ? AND attempt_uid = ?`, at, pipelineRunID, attemptID)
		if updateErr != nil {
			return updateErr
		}
		if err := requireAffected(result, "invalidated stage"); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) AppendEvent(event *Event) error {
	populateEventFields(event)
	result, err := s.db.Exec(`INSERT INTO events (run_uid, sequence, type, stage, actor, executor, severity, attempt_uid, timestamp, data_json, dedupe_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RunID, event.Sequence, event.Type, event.Stage, event.Actor, event.Executor, event.Severity,
		event.AttemptID, event.Timestamp, event.DataJSON, event.DedupeKey)
	if err != nil {
		return err
	}
	event.ID, err = result.LastInsertId()
	if err == nil {
		s.publishEvent(*event)
	}
	return err
}

// AppendEventNext allocates the next per-run sequence inside one transaction,
// so asynchronous queue projection and pipeline recorder events cannot collide.
func (s *Store) AppendEventNext(event *Event) error {
	populateEventFields(event)
	err := s.db.QueryRow(`INSERT INTO events(run_uid, sequence, type, stage, actor, executor, severity, attempt_uid, timestamp, data_json, dedupe_key)
		SELECT ?, COALESCE(MAX(sequence), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM events WHERE run_uid = ? RETURNING id, sequence`,
		event.RunID, event.Type, event.Stage, event.Actor, event.Executor, event.Severity,
		event.AttemptID, event.Timestamp, event.DataJSON, event.DedupeKey, event.RunID).Scan(&event.ID, &event.Sequence)
	if err == nil {
		s.publishEvent(*event)
	}
	return err
}

// AppendEventNextIfAbsent assigns a per-run sequence atomically and uses a
// stable dedupe key to keep timer events exactly once across restarts.
func (s *Store) AppendEventNextIfAbsent(event *Event) (bool, error) {
	if event.DedupeKey == "" {
		return false, fmt.Errorf("event dedupe key is required")
	}
	populateEventFields(event)
	err := s.db.QueryRow(`INSERT INTO events(run_uid, sequence, type, stage, actor, executor, severity, attempt_uid, timestamp, data_json, dedupe_key)
		SELECT ?, COALESCE(MAX(sequence), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM events WHERE run_uid = ?
		AND NOT EXISTS (SELECT 1 FROM events WHERE run_uid = ? AND dedupe_key = ?)
		AND NOT EXISTS (SELECT 1 FROM events WHERE run_uid = ? AND COALESCE(dedupe_key, '') = ''
			AND type = ? AND COALESCE(stage, '') = ? AND COALESCE(attempt_uid, '') = ? AND timestamp = ?)
		ON CONFLICT(run_uid, dedupe_key) WHERE dedupe_key <> '' DO NOTHING RETURNING id, sequence`,
		event.RunID, event.Type, event.Stage, event.Actor, event.Executor, event.Severity,
		event.AttemptID, event.Timestamp, event.DataJSON, event.DedupeKey, event.RunID,
		event.RunID, event.DedupeKey, event.RunID, event.Type, event.Stage, event.AttemptID, event.Timestamp).Scan(&event.ID, &event.Sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.publishEvent(*event)
	return true, nil
}

// IntegrationCursor returns the last durable event cursor acknowledged by an
// event adapter. found is false until the adapter is first enabled.
func (s *Store) IntegrationCursor(integration string) (cursor int64, found bool, err error) {
	if strings.TrimSpace(integration) == "" {
		return 0, false, fmt.Errorf("integration name is required")
	}
	err = s.db.QueryRow(`SELECT cursor FROM integration_cursors WHERE integration = ?`, integration).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return cursor, err == nil, err
}

// SetIntegrationCursor monotonically advances an adapter's acknowledged event
// position. The cursor is committed only after an event was handled or its
// integration_error was durably recorded.
func (s *Store) SetIntegrationCursor(integration string, cursor int64) error {
	if strings.TrimSpace(integration) == "" || cursor < 0 {
		return fmt.Errorf("invalid integration cursor %q=%d", integration, cursor)
	}
	_, err := s.db.Exec(`INSERT INTO integration_cursors(integration, cursor) VALUES (?, ?)
		ON CONFLICT(integration) DO UPDATE SET cursor = MAX(cursor, excluded.cursor)`, integration, cursor)
	return err
}

// NotificationClaimLease bounds how long a claimed but unfinished
// notification blocks other processes. It is well above the per-delivery
// timeout, so it only expires when the claiming process died mid-delivery.
const NotificationClaimLease = 2 * time.Minute

const notificationClaimPrefix = "claimed:"

// ClaimNotification atomically claims a channel/event delivery for this
// process. Every process sharing the database (`ai-team web`, `ai-team run`,
// `ai-team scheduler-worker`) uses the same channel/event key, so only one of
// them sends a given notification. On ClaimAcquired the returned token
// identifies this claim: only its holder can record or release it.
func (s *Store) ClaimNotification(channel string, eventID int64) (notifier.ClaimState, string, error) {
	return s.ClaimNotificationAt(channel, eventID, time.Now().UTC())
}

// ClaimNotificationAt is ClaimNotification with an explicit clock.
func (s *Store) ClaimNotificationAt(channel string, eventID int64, now time.Time) (notifier.ClaimState, string, error) {
	if strings.TrimSpace(channel) == "" || eventID <= 0 {
		return notifier.ClaimBusy, "", fmt.Errorf("invalid notification delivery %q/%d", channel, eventID)
	}
	claim, err := newNotificationClaim(now.Add(NotificationClaimLease))
	if err != nil {
		return notifier.ClaimBusy, "", err
	}
	result, err := s.db.Exec(`INSERT OR IGNORE INTO notification_deliveries(channel, event_id, outcome, recorded_at)
		VALUES (?, ?, ?, ?)`, channel, eventID, claim, now)
	if err != nil {
		return notifier.ClaimBusy, "", err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return notifier.ClaimBusy, "", err
	} else if affected == 1 {
		return notifier.ClaimAcquired, claim, nil
	}
	var current string
	if err := s.db.QueryRow(`SELECT outcome FROM notification_deliveries WHERE channel = ? AND event_id = ?`, channel, eventID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Released between the insert and the read; retry on the next pass.
			return notifier.ClaimBusy, "", nil
		}
		return notifier.ClaimBusy, "", err
	}
	expires, claimed := notificationClaimExpiry(current)
	if !claimed {
		return notifier.ClaimHandled, "", nil
	}
	if now.UnixNano() < expires {
		return notifier.ClaimBusy, "", nil
	}
	// The holder's lease expired: take the claim over with compare-and-swap so
	// two processes reclaiming at once cannot both win. The new token makes
	// any late Record or Release of the previous holder a no-op.
	result, err = s.db.Exec(`UPDATE notification_deliveries SET outcome = ?, recorded_at = ?
		WHERE channel = ? AND event_id = ? AND outcome = ?`, claim, now, channel, eventID, current)
	if err != nil {
		return notifier.ClaimBusy, "", err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return notifier.ClaimBusy, "", err
	}
	return notifier.ClaimAcquired, claim, nil
}

// newNotificationClaim builds a claim token "claimed:<expiry ns>:<nonce>".
// The random nonce makes two claims with the same expiry distinct.
func newNotificationClaim(expires time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("notification claim nonce: %w", err)
	}
	return notificationClaimPrefix + strconv.FormatInt(expires.UnixNano(), 10) + ":" + hex.EncodeToString(nonce), nil
}

// notificationClaimExpiry parses a stored outcome. claimed is false for a
// final outcome; an unparsable claim is treated as already expired.
func notificationClaimExpiry(outcome string) (expires int64, claimed bool) {
	rest, claimed := strings.CutPrefix(outcome, notificationClaimPrefix)
	if !claimed {
		return 0, false
	}
	expiry, _, _ := strings.Cut(rest, ":")
	expires, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return 0, true
	}
	return expires, true
}

// RecordNotification replaces the caller's claim with a final outcome. It
// fails with notifier.ErrClaimLost when token no longer holds the pair: the
// lease expired and another process took it over, or the pair is final.
func (s *Store) RecordNotification(channel string, eventID int64, token, outcome string) error {
	if strings.TrimSpace(channel) == "" || eventID <= 0 || strings.TrimSpace(outcome) == "" ||
		strings.HasPrefix(outcome, notificationClaimPrefix) || !strings.HasPrefix(token, notificationClaimPrefix) {
		return fmt.Errorf("invalid notification delivery %q/%d", channel, eventID)
	}
	result, err := s.db.Exec(`UPDATE notification_deliveries SET outcome = ?, recorded_at = ?
		WHERE channel = ? AND event_id = ? AND outcome = ?`, outcome, time.Now().UTC(), channel, eventID, token)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return notifier.ErrClaimLost
	}
	return nil
}

// ReleaseNotification drops the caller's unfinished claim so the pair can be
// retried. A claim that was taken over or finished is left untouched.
func (s *Store) ReleaseNotification(channel string, eventID int64, token string) error {
	if !strings.HasPrefix(token, notificationClaimPrefix) {
		return fmt.Errorf("invalid notification claim for %q/%d", channel, eventID)
	}
	_, err := s.db.Exec(`DELETE FROM notification_deliveries
		WHERE channel = ? AND event_id = ? AND outcome = ?`, channel, eventID, token)
	return err
}

// NotificationDeliveryRetention is how long final delivery records are kept.
// A record only suppresses a replay of its event, and every cursor moves past
// an event within seconds of a successful delivery, so a month is ample.
const NotificationDeliveryRetention = 30 * 24 * time.Hour

// PruneNotificationDeliveries deletes final delivery records older than
// cutoff and returns how many were removed. Live claims are never removed,
// whatever their age, so pruning cannot let a second process send an event
// that is being delivered. A record is removed only when its event is at or
// below the lowest durable notifier cursor (`notifier` and `notifier:*` in
// integration_cursors): a cursor that lagged longer than the retention period
// may still replay events above it, and their records must keep suppressing
// a second notification. Without any durable notifier cursor nothing can
// replay old events (a new cursor starts at the tail), so age alone decides.
func (s *Store) PruneNotificationDeliveries(cutoff time.Time) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM notification_deliveries
		WHERE recorded_at < ? AND outcome NOT LIKE 'claimed:%'
		AND event_id <= COALESCE((SELECT MIN(cursor) FROM integration_cursors
			WHERE integration = 'notifier' OR integration LIKE 'notifier:%'), 9223372036854775807)`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) publishEvent(event Event) {
	var data map[string]any
	if event.DataJSON != "" {
		_ = json.Unmarshal([]byte(event.DataJSON), &data)
	}
	if data == nil {
		data = map[string]any{}
	}
	s.eventBus.PublishEvent(notifier.Event{
		Cursor: event.ID, RunID: event.RunID, Sequence: event.Sequence, Type: event.Type,
		Stage: event.Stage, Actor: event.Actor, Executor: event.Executor,
		Severity: event.Severity, AttemptID: event.AttemptID,
		Timestamp: event.Timestamp, Data: data,
	})
}

func (s *Store) GetEventsAfter(cursor int64, limit int) ([]Event, error) {
	if cursor < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid event query cursor=%d limit=%d", cursor, limit)
	}
	rows, err := s.db.Query(`SELECT id, run_uid, sequence, type, COALESCE(stage, ''), COALESCE(actor, ''), COALESCE(executor, ''), COALESCE(severity, ''), COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')
		FROM events WHERE id > ? ORDER BY id ASC LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.RunID, &event.Sequence, &event.Type, &event.Stage, &event.Actor, &event.Executor, &event.Severity, &event.AttemptID, &event.Timestamp, &event.DataJSON); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) LatestRunEvent(runID, eventType string) (*Event, error) {
	var event Event
	err := s.db.QueryRow(`SELECT id, run_uid, sequence, type, COALESCE(stage, ''), COALESCE(actor, ''), COALESCE(executor, ''), COALESCE(severity, ''), COALESCE(attempt_uid, ''), timestamp, COALESCE(data_json, '')
		FROM events WHERE run_uid = ? AND type = ? ORDER BY sequence DESC LIMIT 1`, runID, eventType).
		Scan(&event.ID, &event.RunID, &event.Sequence, &event.Type, &event.Stage, &event.Actor, &event.Executor, &event.Severity, &event.AttemptID, &event.Timestamp, &event.DataJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func populateEventFields(event *Event) {
	var data map[string]any
	if event.DataJSON != "" && json.Unmarshal([]byte(event.DataJSON), &data) == nil {
		if event.Stage == "" {
			event.Stage = firstString(data, "stage", "stage_id", "from_stage")
		}
		if event.Actor == "" {
			event.Actor = firstString(data, "actor", "actor_id")
		}
		if event.Executor == "" {
			event.Executor = firstString(data, "executor")
		}
		if event.Severity == "" {
			event.Severity = firstString(data, "severity")
			if event.Severity == "" && data["warning"] == true {
				event.Severity = "warning"
			}
		}
	}
	if event.Severity == "" {
		for _, warningType := range WarningEventTypes {
			if event.Type == warningType {
				event.Severity = "warning"
				break
			}
		}
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func (s *Store) LatestEventCursor() (int64, error) {
	var cursor int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&cursor)
	return cursor, err
}

// WarningEventTypes lists persisted event types that count as task warnings.
// The task API's isTaskWarningEvent shares this list, so the board badges and
// the warnings event feed cannot drift apart.
var WarningEventTypes = []string{
	"check_overridden", "check_error", "description_missing",
	"stage_skipped", "stage_stalled", "integration_error",
	"budget_exceeded", "auto_pickup_deferred", "auto_pickup_failed", "run_resume_failed",
}

// CountRunWarningEvents groups warning events by run for the given run IDs.
// A warning is a typed warning event or any event whose data carries
// severity=warning (mirrors isTaskWarningEvent). One grouped query per small
// chunk keeps the task list responsive with thousands of runs.
func (s *Store) CountRunWarningEvents(runIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(runIDs))
	const chunkSize = 400
	for start := 0; start < len(runIDs); start += chunkSize {
		end := min(start+chunkSize, len(runIDs))
		chunk := runIDs[start:end]
		runPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		typePlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(WarningEventTypes)), ",")
		args := make([]any, 0, len(chunk)+len(WarningEventTypes))
		for _, runID := range chunk {
			args = append(args, runID)
		}
		for _, eventType := range WarningEventTypes {
			args = append(args, eventType)
		}
		rows, err := s.db.Query(`SELECT run_uid, COUNT(*) FROM events
			WHERE run_uid IN (`+runPlaceholders+`)
			AND (type IN (`+typePlaceholders+`) OR LOWER(COALESCE(json_extract(data_json, '$.severity'), '')) = 'warning')
			GROUP BY run_uid`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var runID string
			var count int
			if err := rows.Scan(&runID, &count); err != nil {
				_ = rows.Close()
				return nil, err
			}
			counts[runID] = count
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func (s *Store) GetPipelineRuns() ([]PipelineRun, error) { return s.GetPipelineRunsPage(100, 0) }

func (s *Store) GetPipelineRunsPage(limit, offset int) ([]PipelineRun, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination limit=%d offset=%d", limit, offset)
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '')
		FROM pipeline_runs ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if runs == nil {
		runs = make([]PipelineRun, 0)
	}
	return runs, rows.Err()
}

func (s *Store) CountPipelineRuns() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&count)
	return count, err
}

func (s *Store) GetPipelineRunByID(id int64) (*PipelineRun, error) {
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '') FROM pipeline_runs WHERE id = ?`, id))
}

func (s *Store) GetPipelineRunByRunID(runID string) (*PipelineRun, error) {
	return s.scanRun(s.db.QueryRow(`SELECT id, COALESCE(run_uid, ''), feature, status, started_at, completed_at, COALESCE(config_snapshot, ''), COALESCE(queue_job_id, 0), COALESCE(admission_error, '') FROM pipeline_runs WHERE run_uid = ?`, runID))
}

func (s *Store) scanRun(row *sql.Row) (*PipelineRun, error) {
	var run PipelineRun
	if err := row.Scan(&run.ID, &run.RunID, &run.Feature, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ConfigSnapshot, &run.QueueJobID, &run.Error); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) GetStagesByPipelineRunID(pipelineRunID int64) ([]Stage, error) {
	rows, err := s.db.Query(`SELECT id, pipeline_run_id, COALESCE(attempt_uid, ''), COALESCE(stage_index, 0), agent_name, status,
		COALESCE(execution, ''), COALESCE(decision, ''), COALESCE(outcome, ''), started_at, completed_at, duration_ms,
		COALESCE(error, ''), COALESCE(verdict, ''), COALESCE(inputs_json, ''), COALESCE(outputs_json, ''),
		COALESCE(checks_json, ''), COALESCE(mutations_json, ''), COALESCE(delivery_json, '')
		FROM stages WHERE pipeline_run_id = ? ORDER BY stage_index, id`, pipelineRunID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // курсор освобождается на выходе; значимые ошибки чтения уже обработаны выше.
	var stages []Stage
	for rows.Next() {
		var stage Stage
		if err := rows.Scan(&stage.ID, &stage.PipelineRunID, &stage.AttemptID, &stage.StageIndex, &stage.AgentName, &stage.Status,
			&stage.Execution, &stage.Decision, &stage.Outcome, &stage.StartedAt, &stage.CompletedAt, &stage.DurationMs,
			&stage.Error, &stage.Verdict, &stage.InputsJSON, &stage.OutputsJSON, &stage.ChecksJSON, &stage.MutationsJSON, &stage.DeliveryJSON); err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	if stages == nil {
		stages = make([]Stage, 0)
	}
	return stages, rows.Err()
}

// ReconcileInterrupted is called only after the workspace lock is held.
func (s *Store) ReconcileInterrupted(at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Rollback после успешного Commit возвращает ErrTxDone — штатный исход этого defer.
	if _, err := tx.Exec(`UPDATE stages SET status = 'interrupted', outcome = 'failed', completed_at = ? WHERE status = 'running'`, at); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE pipeline_runs SET status = 'interrupted', completed_at = ? WHERE status = 'running'`, at); err != nil {
		return err
	}
	return tx.Commit()
}

// schemaQuerier is the part of *sql.Conn the migration needs; the migration
// runs on a dedicated connection inside an explicit BEGIN IMMEDIATE.
type schemaQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// storedSchemaVersion reads MAX(version) without writing anything; a database
// without schema_migrations (new or pre-versioning) reports version 0.
func storedSchemaVersion(ctx context.Context, q schemaQuerier) (int64, error) {
	var tables int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	var latest sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil {
		return 0, err
	}
	return latest.Int64, nil
}

func checkSchemaVersion(version int64) error {
	if version > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, SchemaVersion)
	}
	return nil
}

// migrate brings the schema to SchemaVersion. Opening a database that is
// already at SchemaVersion only reads the version and takes no write lock, so
// concurrent opens of a busy database cannot fail with SQLITE_BUSY (BL-33).
// A database with a newer version is refused, so an old binary never opens a
// schema it does not understand. When a migration is needed it runs under
// BEGIN IMMEDIATE: the write lock is taken up front (waiting via
// busy_timeout) instead of upgrading a stale read snapshot, and the version is
// re-read under the lock in case another process migrated first.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	version, err := storedSchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(version); err != nil {
		return err
	}
	if version == SchemaVersion {
		return nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	version, err = storedSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(version); err != nil {
		return err
	}
	if version < SchemaVersion {
		if err := applyMigrations(ctx, conn); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// applyMigrations is idempotent: every statement tolerates a partially
// migrated database, so it can run from any older version.
func applyMigrations(ctx context.Context, tx schemaQuerier) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		return err
	}
	base := []string{
		`CREATE TABLE IF NOT EXISTS pipeline_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, feature TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'running',
			started_at DATETIME NOT NULL, completed_at DATETIME, config_snapshot TEXT)`,
		`CREATE TABLE IF NOT EXISTS stages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, pipeline_run_id INTEGER NOT NULL, agent_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running', started_at DATETIME NOT NULL, completed_at DATETIME,
			duration_ms INTEGER DEFAULT 0, error TEXT, verdict TEXT, inputs_json TEXT, outputs_json TEXT,
			FOREIGN KEY (pipeline_run_id) REFERENCES pipeline_runs(id))`,
	}
	for _, query := range base {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	columns := []struct{ table, name, declaration string }{
		{"pipeline_runs", "run_uid", "TEXT"},
		{"pipeline_runs", "queue_job_id", "INTEGER DEFAULT 0"}, {"pipeline_runs", "admission_error", "TEXT"},
		{"stages", "attempt_uid", "TEXT"}, {"stages", "stage_index", "INTEGER DEFAULT 0"},
		{"stages", "execution", "TEXT"}, {"stages", "decision", "TEXT"}, {"stages", "outcome", "TEXT"},
		{"stages", "checks_json", "TEXT"}, {"stages", "mutations_json", "TEXT"}, {"stages", "delivery_json", "TEXT"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.declaration); err != nil {
			return err
		}
	}
	queries := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_runs_uid ON pipeline_runs(run_uid) WHERE run_uid IS NOT NULL AND run_uid <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_runs_started ON pipeline_runs(started_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_stages_run ON stages(pipeline_run_id, stage_index, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_stages_attempt ON stages(attempt_uid) WHERE attempt_uid IS NOT NULL AND attempt_uid <> ''`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, run_uid TEXT NOT NULL, sequence INTEGER NOT NULL, type TEXT NOT NULL,
			attempt_uid TEXT, timestamp DATETIME NOT NULL, data_json TEXT,
			UNIQUE(run_uid, sequence))`,
		`CREATE INDEX IF NOT EXISTS idx_events_run ON events(run_uid, sequence)`,
		`CREATE TABLE IF NOT EXISTS integration_cursors (
			integration TEXT PRIMARY KEY, cursor INTEGER NOT NULL CHECK(cursor >= 0))`,
		`CREATE TABLE IF NOT EXISTS notification_deliveries (
			channel TEXT NOT NULL, event_id INTEGER NOT NULL, outcome TEXT NOT NULL,
			recorded_at DATETIME NOT NULL, PRIMARY KEY(channel, event_id))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_dedupe ON events(run_uid, dedupe_key) WHERE dedupe_key <> ''`,
		`CREATE TABLE IF NOT EXISTS team_members (
			actor_id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, roles_json TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('invited','active','revoked')),
			session_epoch INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS team_invitations (
			token_hash TEXT PRIMARY KEY, actor_id TEXT NOT NULL, expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL, used_at DATETIME, invited_by TEXT NOT NULL,
			FOREIGN KEY(actor_id) REFERENCES team_members(actor_id))`,
		`CREATE TABLE IF NOT EXISTS team_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT, actor_id TEXT NOT NULL, action TEXT NOT NULL,
			target_actor_id TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '{}', created_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_idempotency (
			scope TEXT NOT NULL, request_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
			status_code INTEGER NOT NULL DEFAULT 0, response_headers TEXT NOT NULL DEFAULT '{}', response_body BLOB,
			created_at DATETIME NOT NULL, PRIMARY KEY(scope, request_key))`,
		// Version 10 (B-83): durable browser sessions and pending OIDC sign-ins.
		// Binaries with SchemaVersion < 10 refuse to open the upgraded file, so
		// an old server cannot silently ignore persisted sessions or revocations.
		`CREATE TABLE IF NOT EXISTS web_sessions (
			token_hash TEXT PRIMARY KEY, actor_id TEXT NOT NULL, roles_json TEXT NOT NULL,
			session_epoch INTEGER NOT NULL DEFAULT 0, auth_method TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '',
			csrf_token TEXT NOT NULL, credential_binding TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
			absolute_expires_at INTEGER NOT NULL, revoked_at INTEGER)`,
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_expires ON web_sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS oidc_login_states (
			state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, code_verifier TEXT NOT NULL,
			previous_session_hash TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		// Version 11 (B-86): draft idea backlog for auto pickup.
		`CREATE TABLE IF NOT EXISTS backlog_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, idea TEXT NOT NULL, feature TEXT NOT NULL,
			status TEXT NOT NULL, created_by TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
			ready_by TEXT NOT NULL DEFAULT '', ready_at DATETIME, run_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS idx_backlog_queue ON backlog_items(status, ready_at, id)`,
		// Version 12 (BL-25): the task-scoped event feed filters by run_uid and
		// pages by the global event ID in both directions; without this index
		// SQLite walks the whole events table for every page. IF NOT EXISTS
		// keeps reopening an upgraded file a no-op.
		`CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_uid, id)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, CURRENT_TIMESTAMP), (2, CURRENT_TIMESTAMP), (3, CURRENT_TIMESTAMP), (4, CURRENT_TIMESTAMP), (5, CURRENT_TIMESTAMP), (6, CURRENT_TIMESTAMP), (7, CURRENT_TIMESTAMP), (8, CURRENT_TIMESTAMP), (9, CURRENT_TIMESTAMP), (10, CURRENT_TIMESTAMP), (11, CURRENT_TIMESTAMP), (12, CURRENT_TIMESTAMP), (13, CURRENT_TIMESTAMP)`,
	}
	for _, query := range queries {
		if strings.Contains(query, "idx_events_dedupe") {
			for _, column := range []struct{ name, declaration string }{
				{"stage", "TEXT"}, {"actor", "TEXT"}, {"executor", "TEXT"}, {"severity", "TEXT"}, {"dedupe_key", "TEXT"},
			} {
				if err := ensureColumn(ctx, tx, "events", column.name, column.declaration); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE events SET
				stage = COALESCE(NULLIF(stage, ''), json_extract(data_json, '$.stage'), json_extract(data_json, '$.stage_id'), json_extract(data_json, '$.from_stage'), ''),
				actor = COALESCE(NULLIF(actor, ''), json_extract(data_json, '$.actor'), json_extract(data_json, '$.actor_id'), ''),
				executor = COALESCE(NULLIF(executor, ''), json_extract(data_json, '$.executor'), ''),
				severity = COALESCE(NULLIF(severity, ''), json_extract(data_json, '$.severity'), CASE WHEN json_extract(data_json, '$.warning') = 1 OR type IN ('check_overridden', 'check_error', 'description_missing', 'stage_skipped', 'stage_stalled', 'integration_error') THEN 'warning' ELSE '' END)
				WHERE json_valid(data_json)`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return migrateV13(ctx, tx)
}

// migrateV13 (BL-31) adds the SSO account binding of team members, the
// previous-session digest of pending sign-ins and, for local databases
// created by an early version 10 build, web_sessions.credential_binding. The
// empty credential_binding default never matches a server binding, so such
// sessions are rejected (fail closed). Every statement is idempotent.
func migrateV13(ctx context.Context, tx schemaQuerier) error {
	for _, column := range []struct{ table, name, declaration string }{
		{"web_sessions", "credential_binding", "TEXT NOT NULL DEFAULT ''"},
		{"oidc_login_states", "previous_session_hash", "TEXT NOT NULL DEFAULT ''"},
		{"team_members", "oidc_issuer", "TEXT NOT NULL DEFAULT ''"},
		{"team_members", "oidc_subject", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.declaration); err != nil {
			return err
		}
	}
	// One provider account binds to at most one team member.
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_team_members_oidc ON team_members(oidc_issuer, oidc_subject) WHERE oidc_subject <> ''`); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, tx schemaQuerier, table, name, declaration string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		exists = exists || columnName == name
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+declaration)
	return err
}

func requireAffected(result sql.Result, entity string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s update affected %d rows", entity, affected)
	}
	return nil
}
