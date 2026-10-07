package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BackupDatabase writes an online, consistent SQLite snapshot to a new path.
// It includes committed WAL content, but intentionally backs up only this
// database file; run evidence, artifacts, and other state require separate
// backup handling.
func BackupDatabase(ctx context.Context, sourcePath, destination string) (retErr error) {
	if ctx == nil {
		return errors.New("database backup context is nil")
	}
	if sourcePath == "" || destination == "" {
		return errors.New("database backup source and destination are required")
	}
	source, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve database backup source: %w", err)
	}
	dest, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve database backup destination: %w", err)
	}
	if source == dest {
		return errors.New("database backup destination must differ from source")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect database backup source: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return errors.New("database backup source must be a regular file")
	}
	if info, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("database backup destination already exists (%s)", info.Mode().Type())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect database backup destination: %w", err)
	}
	parent := filepath.Dir(dest)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect database backup directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return errors.New("database backup destination parent is not a directory")
	}
	tmpDir, err := os.MkdirTemp(parent, ".ai-team-db-backup-")
	if err != nil {
		return fmt.Errorf("create private database backup staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove database backup staging directory: %w", err))
		}
	}()
	tmpPath := filepath.Join(tmpDir, "snapshot.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return fmt.Errorf("open database for backup: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return errors.Join(fmt.Errorf("configure database backup busy timeout: %w", err), db.Close())
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmpPath); err != nil {
		return errors.Join(fmt.Errorf("create consistent SQLite backup: %w", err), db.Close())
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close database after backup: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return fmt.Errorf("secure database backup file: %w", err)
	}
	file, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("open completed database backup for sync: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync completed database backup: %w", err)
	}
	dir, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open database backup directory for sync: %w", err)
	}
	if err := os.Link(tmpPath, dest); err != nil {
		return errors.Join(fmt.Errorf("publish database backup without replacing an existing file: %w", err), dir.Close())
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// RestoreDatabaseSnapshot validates a SQLite snapshot and writes it to a new
// database path without replacing existing files. It restores only database
// contents; callers must restore evidence and artifacts separately.
func RestoreDatabaseSnapshot(ctx context.Context, snapshotPath, destination string) error {
	if ctx == nil {
		return errors.New("database restore context is nil")
	}
	if snapshotPath == "" || destination == "" {
		return errors.New("database restore snapshot and destination are required")
	}
	snapshot, err := filepath.Abs(snapshotPath)
	if err != nil {
		return fmt.Errorf("resolve database restore snapshot: %w", err)
	}
	info, err := os.Lstat(snapshot)
	if err != nil {
		return fmt.Errorf("inspect database restore snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("database restore snapshot must be a regular file")
	}
	db, err := sql.Open("sqlite", snapshot)
	if err != nil {
		return fmt.Errorf("open SQLite restore snapshot: %w", err)
	}
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return errors.Join(fmt.Errorf("check SQLite restore snapshot integrity: %w", err), db.Close())
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close SQLite restore snapshot after integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite restore snapshot failed integrity check: %s", integrity)
	}
	return BackupDatabase(ctx, snapshot, destination)
}
