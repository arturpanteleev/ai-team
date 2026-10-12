package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
)

// ErrBacklogTransition — недопустимая смена статуса элемента бэклога.
var ErrBacklogTransition = errors.New("backlog status transition is not allowed")

// BacklogItem — идея в черновом бэклоге. Бэклог хранится отдельно от задач:
// элемент становится задачей только после явной пометки ready человеком и
// захвата подборщиком.
type BacklogItem struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title"`
	Idea      string     `json:"idea"`
	Feature   string     `json:"feature"`
	Status    string     `json:"status"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ReadyBy   string     `json:"ready_by,omitempty"`
	ReadyAt   *time.Time `json:"ready_at,omitempty"`
	RunID     string     `json:"run_id,omitempty"`
	Error     string     `json:"error,omitempty"`
}

const backlogColumns = `id, title, idea, feature, status, created_by, created_at, updated_at,
	ready_by, ready_at, run_id, error`

func scanBacklogItem(scanner interface{ Scan(...any) error }) (BacklogItem, error) {
	var item BacklogItem
	var readyAt sql.NullTime
	if err := scanner.Scan(&item.ID, &item.Title, &item.Idea, &item.Feature, &item.Status, &item.CreatedBy,
		&item.CreatedAt, &item.UpdatedAt, &item.ReadyBy, &readyAt, &item.RunID, &item.Error); err != nil {
		return BacklogItem{}, err
	}
	if readyAt.Valid {
		value := readyAt.Time.UTC()
		item.ReadyAt = &value
	}
	item.CreatedAt, item.UpdatedAt = item.CreatedAt.UTC(), item.UpdatedAt.UTC()
	return item, nil
}

// CreateBacklogItem сохраняет новую идею всегда как черновик: создание не
// может сразу сделать её доступной для автоподбора.
func (s *Store) CreateBacklogItem(title, idea, feature, actorID string, at time.Time) (BacklogItem, error) {
	at = at.UTC()
	result, err := s.db.Exec(`INSERT INTO backlog_items (title, idea, feature, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, title, idea, feature, autopickup.StatusDraft, actorID, at, at)
	if err != nil {
		return BacklogItem{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return BacklogItem{}, err
	}
	return s.GetBacklogItem(id)
}

// GetBacklogItem возвращает элемент по идентификатору.
func (s *Store) GetBacklogItem(id int64) (BacklogItem, error) {
	return scanBacklogItem(s.db.QueryRow(`SELECT `+backlogColumns+` FROM backlog_items WHERE id = ?`, id))
}

// ListBacklogItems возвращает бэклог в порядке создания.
func (s *Store) ListBacklogItems() ([]BacklogItem, error) {
	rows, err := s.db.Query(`SELECT ` + backlogColumns + ` FROM backlog_items ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]BacklogItem, 0)
	for rows.Next() {
		item, err := scanBacklogItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SetBacklogItemStatus применяет переход, разрешённый человеку. Сравнение
// со старым статусом входит в UPDATE, поэтому гонка с подборщиком не может
// вернуть захваченный элемент в очередь.
func (s *Store) SetBacklogItemStatus(id int64, status, actorID string, at time.Time) (BacklogItem, error) {
	current, err := s.GetBacklogItem(id)
	if err != nil {
		return BacklogItem{}, err
	}
	if !autopickup.HumanTransitionAllowed(current.Status, status) {
		return BacklogItem{}, fmt.Errorf("%w: %s -> %s", ErrBacklogTransition, current.Status, status)
	}
	at = at.UTC()
	query := `UPDATE backlog_items SET status = ?, updated_at = ?, error = '' WHERE id = ? AND status = ?`
	args := []any{status, at, id, current.Status}
	if status == autopickup.StatusReady {
		// Позиция в очереди — момент явной пометки человеком.
		// Связь с неудавшимся запуском снимается: следующий захват свяжет
		// элемент с новым run.
		query = `UPDATE backlog_items SET status = ?, updated_at = ?, error = '', run_id = '', ready_by = ?, ready_at = ?
			WHERE id = ? AND status = ?`
		args = []any{status, at, actorID, at, id, current.Status}
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return BacklogItem{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return BacklogItem{}, err
	} else if affected != 1 {
		return BacklogItem{}, fmt.Errorf("%w: status changed concurrently", ErrBacklogTransition)
	}
	return s.GetBacklogItem(id)
}

// NextReadyBacklogItem возвращает первый элемент очереди автоподбора.
// Берётся только точное значение ready: неизвестный или искажённый признак
// (например, "READY" из ручной правки базы) элемент не делает доступным.
func (s *Store) NextReadyBacklogItem() (BacklogItem, bool, error) {
	item, err := scanBacklogItem(s.db.QueryRow(`SELECT `+backlogColumns+` FROM backlog_items
		WHERE status = ? ORDER BY ready_at, id LIMIT 1`, autopickup.StatusReady))
	if errors.Is(err, sql.ErrNoRows) {
		return BacklogItem{}, false, nil
	}
	if err != nil {
		return BacklogItem{}, false, err
	}
	return item, true, nil
}

// ClaimBacklogItem атомарно переводит ready → starting. false — элемент уже
// забрали или человек снял пометку.
func (s *Store) ClaimBacklogItem(id int64, at time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE backlog_items SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		autopickup.StatusStarting, at.UTC(), id, autopickup.StatusReady)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// BindBacklogRun связывает захваченный элемент с run до того, как run станет
// видим и запущен: после краха связь восстанавливается без повторного запуска.
func (s *Store) BindBacklogRun(id int64, runID string, at time.Time) error {
	result, err := s.db.Exec(`UPDATE backlog_items SET run_id = ?, updated_at = ? WHERE id = ? AND status = ? AND run_id = ''`,
		runID, at.UTC(), id, autopickup.StatusStarting)
	if err != nil {
		return err
	}
	return requireAffected(result, "backlog item")
}

// FinishBacklogClaim завершает захват: started или failed с причиной.
func (s *Store) FinishBacklogClaim(id int64, status, cause string, at time.Time) error {
	if status != autopickup.StatusStarted && status != autopickup.StatusFailed {
		return fmt.Errorf("unsupported backlog claim result %q", status)
	}
	result, err := s.db.Exec(`UPDATE backlog_items SET status = ?, error = ?, updated_at = ? WHERE id = ? AND status = ?`,
		status, cause, at.UTC(), id, autopickup.StatusStarting)
	if err != nil {
		return err
	}
	return requireAffected(result, "backlog item")
}

// StartingBacklogItems возвращает захваты, прерванные рестартом.
func (s *Store) StartingBacklogItems() ([]BacklogItem, error) {
	rows, err := s.db.Query(`SELECT `+backlogColumns+` FROM backlog_items WHERE status = ? ORDER BY id`, autopickup.StatusStarting)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []BacklogItem
	for rows.Next() {
		item, err := scanBacklogItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ValidBacklogText ограничивает поля идеи разумными размерами.
func ValidBacklogText(value string, limit int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(value) <= limit
}
