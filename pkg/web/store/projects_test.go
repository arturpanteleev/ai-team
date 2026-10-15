package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanInstallHasDefaultProject(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	projects, err := s.ListProjects(true)
	if err != nil || len(projects) != 1 {
		t.Fatalf("clean install projects = %+v err=%v", projects, err)
	}
	if projects[0].ID != DefaultProjectID || projects[0].Name != DefaultProjectName || projects[0].Archived {
		t.Fatalf("default project = %+v", projects[0])
	}
}

// Версия 13 (до проектов): задачи и идеи без project_id. Миграция создаёт
// «Основной» и назначает им все старые записи, ничего не теряя.
func TestVersion13DatabaseMigratesToProjects(t *testing.T) {
	if SchemaVersion < 14 {
		t.Fatalf("test written for the 13 -> 14 step, SchemaVersion=%d", SchemaVersion)
	}
	path := filepath.Join(t.TempDir(), "web.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := s.CreatePipelineRun(&PipelineRun{RunID: "old-run", Feature: "old", Status: "completed", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateBacklogItem(DefaultProjectID, "Старая идея", "", "old-idea", "po", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP INDEX idx_pipeline_runs_project`,
		`DROP INDEX idx_backlog_items_project`,
		`ALTER TABLE pipeline_runs DROP COLUMN project_id`,
		`ALTER TABLE backlog_items DROP COLUMN project_id`,
		`DROP TABLE projects`,
		`DELETE FROM schema_migrations WHERE version > 13`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = New(path)
	if err != nil {
		t.Fatalf("v13 database must migrate: %v", err)
	}
	defer func() { _ = s.Close() }()
	var latest int64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != SchemaVersion {
		t.Fatalf("schema version after migration = %d (%v), want %d", latest, err, SchemaVersion)
	}
	projects, err := s.ListProjects(true)
	if err != nil || len(projects) != 1 || projects[0].ID != DefaultProjectID || projects[0].Name != DefaultProjectName {
		t.Fatalf("migration must create exactly the default project: %+v %v", projects, err)
	}
	run, err := s.GetPipelineRunByRunID("old-run")
	if err != nil || run.ProjectID != DefaultProjectID {
		t.Fatalf("old run must belong to the default project: %+v %v", run, err)
	}
	migrated, err := s.GetBacklogItem(item.ID)
	if err != nil || migrated.ProjectID != DefaultProjectID || migrated.Title != "Старая идея" {
		t.Fatalf("old backlog item must belong to the default project: %+v %v", migrated, err)
	}
	// Повторное открытие идемпотентно и не плодит проекты.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	if projects, err := s.ListProjects(true); err != nil || len(projects) != 1 {
		t.Fatalf("reopen duplicated projects: %+v %v", projects, err)
	}
}

func TestProjectCRUDAndArchive(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	project, err := s.CreateProject(" Платформа ", "описание", "po-1", at)
	if err != nil || project.Name != "Платформа" || project.CreatedBy != "po-1" || project.Archived {
		t.Fatalf("create: %+v %v", project, err)
	}
	if _, err := s.CreateProject("ПЛАТФОРМА", "", "po-1", at); !errors.Is(err, ErrProjectNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.UpdateProject(DefaultProjectID, "платформа", "", at); !errors.Is(err, ErrProjectNameTaken) {
		t.Fatalf("rename to a taken name: %v", err)
	}
	if _, err := s.UpdateProject(999, "x", "", at); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	if _, err := s.SetProjectArchived(999, true, at); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("archive unknown: %v", err)
	}

	archivedAt := at.Add(time.Hour)
	archived, err := s.SetProjectArchived(project.ID, true, archivedAt)
	if err != nil || !archived.Archived || !archived.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	again, err := s.SetProjectArchived(project.ID, true, archivedAt.Add(time.Hour))
	if err != nil || !again.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("repeat archive must keep the first time: %+v %v", again, err)
	}
	if active, _ := s.ListProjects(false); len(active) != 1 {
		t.Fatalf("active list: %+v", active)
	}

	if err := s.RequireActiveProject(project.ID); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("archived: %v", err)
	}
	if err := s.RequireActiveProject(0); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("zero: %v", err)
	}
	if err := s.RequireActiveProject(999); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	run := &PipelineRun{RunID: "r", ProjectID: project.ID, Feature: "f", Status: "queued", StartedAt: at}
	if err := s.AdmitPipelineRun(run); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("admit into archived project: %v", err)
	}
	if err := s.AdmitPipelineRun(&PipelineRun{RunID: "r0", Feature: "f", Status: "queued", StartedAt: at}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("admit without project: %v", err)
	}
	if _, err := s.CreateBacklogItem(project.ID, "t", "", "f", "po", at); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("backlog into archived project: %v", err)
	}
	if _, err := s.CreateBacklogItem(0, "t", "", "f", "po", at); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("backlog without project: %v", err)
	}

	// История архивного проекта читается: уже принятая задача остаётся.
	if _, err := s.SetProjectArchived(project.ID, false, at); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitPipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectArchived(project.ID, true, at); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetPipelineRunByRunID("r")
	if err != nil || stored.ProjectID != project.ID {
		t.Fatalf("history of an archived project must stay readable: %+v %v", stored, err)
	}
}
