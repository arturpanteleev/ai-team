package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Проект первой версии — только папка для группировки задач: ID, имя,
// описание и архивный признак. Репозитория, шаблона и прав у проекта нет.
const (
	// DefaultProjectID — проект «Основной». Он создаётся миграцией и получает
	// все записи, появившиеся до введения проектов.
	DefaultProjectID          int64 = 1
	DefaultProjectName              = "Основной"
	DefaultProjectDescription       = "Проект для задач, созданных до появления проектов."

	MaxProjectNameBytes        = 120
	MaxProjectDescriptionBytes = 2000
)

var (
	// ErrProjectRequired — у задачи или идеи не указан проект.
	ErrProjectRequired = errors.New("project_id is required")
	// ErrProjectNotFound — проекта с таким ID нет.
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectArchived — архивный проект не принимает новые задачи.
	ErrProjectArchived = errors.New("project is archived and does not accept new tasks")
	// ErrProjectNameTaken — имя проекта (без учёта регистра) уже занято.
	ErrProjectNameTaken = errors.New("project name is already in use")
)

// Project — папка проектов. Архивный проект остаётся читаемым вместе с
// историей задач, но не принимает новые задачи и идеи.
type Project struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Archived    bool       `json:"archived"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const projectColumns = `id, name, description, archived_at, created_by, created_at, updated_at`

type projectRowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func scanProject(scanner interface{ Scan(...any) error }) (Project, error) {
	var project Project
	var archivedAt sql.NullTime
	if err := scanner.Scan(&project.ID, &project.Name, &project.Description, &archivedAt,
		&project.CreatedBy, &project.CreatedAt, &project.UpdatedAt); err != nil {
		return Project{}, err
	}
	if archivedAt.Valid {
		value := archivedAt.Time.UTC()
		project.Archived, project.ArchivedAt = true, &value
	}
	project.CreatedAt, project.UpdatedAt = project.CreatedAt.UTC(), project.UpdatedAt.UTC()
	return project, nil
}

// requireActiveProject проверяет, что проект существует и не архивный. Для
// ID <= 0 возвращает ErrProjectRequired, чтобы пропущенное поле не маскировалось
// под «не найден».
func requireActiveProject(q projectRowQuerier, id int64) error {
	if id <= 0 {
		return ErrProjectRequired
	}
	var archivedAt sql.NullTime
	err := q.QueryRow(`SELECT archived_at FROM projects WHERE id = ?`, id).Scan(&archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectNotFound
	}
	if err != nil {
		return err
	}
	if archivedAt.Valid {
		return ErrProjectArchived
	}
	return nil
}

// RequireActiveProject — публичная проверка для раннего отказа в API.
func (s *Store) RequireActiveProject(id int64) error { return requireActiveProject(s.db, id) }

// ValidProjectName проверяет имя проекта: непустое после обрезки пробелов и в пределах лимита.
func ValidProjectName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && len(trimmed) <= MaxProjectNameBytes
}

// projectNameKey — ключ уникальности имени: без пробелов по краям и без учёта
// регистра для любого алфавита (NOCASE в SQLite сворачивает только ASCII).
func projectNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateProject создаёт активный проект. Имя уникально без учёта регистра.
func (s *Store) CreateProject(name, description, actorID string, at time.Time) (Project, error) {
	at = at.UTC()
	result, err := s.db.Exec(`INSERT INTO projects (name, name_key, description, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, strings.TrimSpace(name), projectNameKey(name), description, actorID, at, at)
	if isUniqueViolation(err) {
		return Project{}, ErrProjectNameTaken
	}
	if err != nil {
		return Project{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(id)
}

// GetProject возвращает проект; ErrProjectNotFound, если его нет.
func (s *Store) GetProject(id int64) (Project, error) {
	project, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	return project, err
}

// ListProjects возвращает проекты в порядке создания; архивные — только по includeArchived.
func (s *Store) ListProjects(includeArchived bool) ([]Project, error) {
	query := `SELECT ` + projectColumns + ` FROM projects`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	rows, err := s.db.Query(query + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// UpdateProject меняет имя и описание проекта; архивность не затрагивает.
func (s *Store) UpdateProject(id int64, name, description string, at time.Time) (Project, error) {
	result, err := s.db.Exec(`UPDATE projects SET name = ?, name_key = ?, description = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(name), projectNameKey(name), description, at.UTC(), id)
	if isUniqueViolation(err) {
		return Project{}, ErrProjectNameTaken
	}
	if err != nil {
		return Project{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Project{}, err
	} else if affected == 0 {
		return Project{}, ErrProjectNotFound
	}
	return s.GetProject(id)
}

// SetProjectArchived архивирует проект или возвращает его из архива.
// Физического удаления нет: задачи и история проекта сохраняются. Повтор
// идемпотентен и сохраняет исходное время архивирования.
func (s *Store) SetProjectArchived(id int64, archived bool, at time.Time) (Project, error) {
	var result sql.Result
	var err error
	if archived {
		result, err = s.db.Exec(`UPDATE projects SET
			updated_at = CASE WHEN archived_at IS NULL THEN ? ELSE updated_at END,
			archived_at = COALESCE(archived_at, ?) WHERE id = ?`, at.UTC(), at.UTC(), id)
	} else {
		result, err = s.db.Exec(`UPDATE projects SET
			updated_at = CASE WHEN archived_at IS NOT NULL THEN ? ELSE updated_at END,
			archived_at = NULL WHERE id = ?`, at.UTC(), id)
	}
	if err != nil {
		return Project{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Project{}, err
	} else if affected == 0 {
		return Project{}, fmt.Errorf("%w: %d", ErrProjectNotFound, id)
	}
	return s.GetProject(id)
}
