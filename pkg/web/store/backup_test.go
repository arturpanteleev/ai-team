package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupDatabaseCapturesCommittedWALState(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "controller.db")
	sourceStore, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceStore.db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	run := &PipelineRun{RunID: "backup-run", Feature: "backup", Status: "queued", StartedAt: time.Now().UTC(), ConfigSnapshot: `{"schema_version":1}`}
	if err := sourceStore.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	walInfo, err := os.Stat(source + "-wal")
	if err != nil || walInfo.Size() <= 32 {
		t.Fatalf("test database did not retain committed WAL state: info=%v err=%v", walInfo, err)
	}
	destination := filepath.Join(dir, "backups", "controller.snapshot.db")
	if err := os.Mkdir(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := BackupDatabase(context.Background(), source, destination); err != nil {
		t.Fatalf("online backup: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup file mode=%#o, want 0600", info.Mode().Perm())
	}
	if err := sourceStore.Close(); err != nil {
		t.Fatal(err)
	}
	backupStore, err := New(destination)
	if err != nil {
		t.Fatalf("open database snapshot: %v", err)
	}
	defer func() {
		if err := backupStore.Close(); err != nil {
			t.Errorf("close database snapshot: %v", err)
		}
	}()
	got, err := backupStore.GetPipelineRunByRunID(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Feature != run.Feature || got.Status != run.Status || got.ConfigSnapshot != run.ConfigSnapshot {
		t.Fatalf("backup lost committed row: got %+v, want %+v", got, run)
	}
	if err := BackupDatabase(context.Background(), source, destination); err == nil {
		t.Fatal("backup replaced an existing destination")
	}
}

func TestBackupDatabaseRejectsUnsafeArguments(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "controller.db")
	store, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		ctx         context.Context
		source      string
		destination string
	}{
		{name: "nil context", source: source, destination: filepath.Join(dir, "nil.db")},
		{name: "same file", ctx: context.Background(), source: source, destination: source},
		{name: "missing source", ctx: context.Background(), source: filepath.Join(dir, "missing.db"), destination: filepath.Join(dir, "missing-out.db")},
		{name: "symlink source", ctx: context.Background(), source: filepath.Join(dir, "source-link.db"), destination: filepath.Join(dir, "link-out.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink source" {
				if err := os.Symlink(source, tc.source); err != nil {
					t.Fatal(err)
				}
			}
			if err := BackupDatabase(tc.ctx, tc.source, tc.destination); err == nil {
				t.Fatal("unsafe backup arguments unexpectedly succeeded")
			}
		})
	}
}
