//go:build linux || darwin

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBriefFileAtRejectsUnsafeFilesAndEnforcesLimit(t *testing.T) {
	root := t.TempDir()
	directoryFD, err := openBriefDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeBriefFD(directoryFD); err != nil {
			t.Errorf("close brief test directory: %v", err)
		}
	})

	writeReadOnly := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
		return path
	}

	validPath := writeReadOnly("valid.md", []byte("brief"))
	if got, err := readBriefFileAt(directoryFD, filepath.Base(validPath), 5); err != nil || string(got) != "brief" {
		t.Fatalf("read immutable brief got=%q err=%v", got, err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(validPath), 4); err == nil {
		t.Fatal("read accepted a brief larger than the caller's limit")
	}

	mutablePath := filepath.Join(root, "mutable.md")
	if err := os.WriteFile(mutablePath, []byte("mutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(mutablePath), 64); err == nil {
		t.Fatal("read accepted a writable brief file")
	}

	hardlinkPath := writeReadOnly("hardlinked.md", []byte("linked"))
	if err := os.Link(hardlinkPath, filepath.Join(root, "hardlink-alias.md")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := readBriefFileAt(directoryFD, filepath.Base(hardlinkPath), 64); err == nil {
		t.Fatal("read accepted a brief file with multiple hard links")
	}

	if err := os.Symlink(validPath, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readBriefFileAt(directoryFD, "linked.md", 64); err == nil {
		t.Fatal("read followed a symlinked brief file")
	}

	if err := os.Mkdir(filepath.Join(root, "directory.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readBriefFileAt(directoryFD, "directory.md", 64); err == nil {
		t.Fatal("read accepted a directory as a brief file")
	}
	if _, err := readBriefFileAt(directoryFD, "missing.md", 64); err == nil {
		t.Fatal("read accepted a missing brief file")
	}
}

func TestSecureMigrateOpenFailureRestoresMovedLeafWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "canonical")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonicalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const runID = "run-one"
	leaf := filepath.Join(canonicalRoot, runID)
	if err := os.WriteFile(leaf, []byte("substituted user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyFD, err := openBriefDirectory(root, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(legacyFD); err != nil {
			t.Errorf("close legacy test directory: %v", err)
		}
	}()
	canonicalFD, err := openBriefDirectory(root, "canonical")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(canonicalFD); err != nil {
			t.Errorf("close canonical test directory: %v", err)
		}
	}()

	if err := restoreMovedBriefAfterOpenFailure(legacyFD, canonicalFD, runID, os.ErrNotExist); err == nil {
		t.Fatal("migration verification failure was not returned")
	}
	got, err := os.ReadFile(filepath.Join(legacyRoot, "brief"))
	if err != nil || string(got) != "substituted user data" {
		t.Fatalf("unexpected moved leaf was not restored intact: data=%q err=%v", got, err)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Fatalf("unexpected canonical leaf remains after rollback: err=%v", err)
	}
}

func TestRemoveBriefDirectoryContentsRestoresChangedEntryFromQuarantine(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "legacy-brief")
	quarantineRoot := filepath.Join(root, "quarantine")
	if err := os.Mkdir(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(quarantineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const name = "0001-intention.md"
	const changed = "replacement after initial validation"
	if err := os.WriteFile(filepath.Join(sourceRoot, name), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sourceRoot, name), 0o444); err != nil {
		t.Fatal(err)
	}
	sourceFD, err := openBriefDirectory(root, "legacy-brief")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(sourceFD); err != nil {
			t.Errorf("close legacy brief test directory: %v", err)
		}
	}()
	quarantineFD, err := openBriefDirectory(root, "quarantine")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(quarantineFD); err != nil {
			t.Errorf("close quarantine test directory: %v", err)
		}
	}()

	err = removeBriefDirectoryContents(sourceFD, quarantineFD, map[string][]byte{name: []byte("previously validated content")})
	if err == nil {
		t.Fatal("cleanup accepted a changed entry")
	}
	got, readErr := os.ReadFile(filepath.Join(sourceRoot, name))
	if readErr != nil || string(got) != changed {
		t.Fatalf("changed entry was not restored intact: data=%q err=%v", got, readErr)
	}
	entries, readErr := os.ReadDir(quarantineRoot)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("quarantine should be empty after restoring changed entry: entries=%v err=%v", entries, readErr)
	}
}

func TestRemoveEmptyLegacyBriefDirectoryRestoresUnexpectedLeaf(t *testing.T) {
	root := t.TempDir()
	runRoot := filepath.Join(root, "run")
	canonicalRoot := filepath.Join(root, "canonical")
	pinnedRoot := filepath.Join(root, "pinned-original")
	for _, path := range []string{runRoot, canonicalRoot, pinnedRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const substituted = "user file substituted for brief directory"
	if err := os.WriteFile(filepath.Join(runRoot, "brief"), []byte(substituted), 0o600); err != nil {
		t.Fatal(err)
	}
	runFD, err := openBriefDirectory(root, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(runFD); err != nil {
			t.Errorf("close run test directory: %v", err)
		}
	}()
	canonicalFD, err := openBriefDirectory(root, "canonical")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(canonicalFD); err != nil {
			t.Errorf("close canonical test directory: %v", err)
		}
	}()
	pinnedFD, err := openBriefDirectory(root, "pinned-original")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeBriefFD(pinnedFD); err != nil {
			t.Errorf("close pinned test directory: %v", err)
		}
	}()

	if err := removeEmptyLegacyBriefDirectory(runFD, canonicalFD, pinnedFD); err == nil {
		t.Fatal("cleanup accepted a substituted non-directory leaf")
	}
	got, err := os.ReadFile(filepath.Join(runRoot, "brief"))
	if err != nil || string(got) != substituted {
		t.Fatalf("substituted leaf was not restored intact: data=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(canonicalRoot, legacyBriefDirectoryQuarantineEntry)); !os.IsNotExist(err) {
		t.Fatalf("quarantined substitute remains after rollback: err=%v", err)
	}
}

func TestRecoverLegacyBriefQuarantineBeforeReadingCanonicalTree(t *testing.T) {
	target := t.TempDir()
	const runID = "quarantine-recovery"
	canonicalRoot := filepath.Join(target, ".ai-team", "state", "briefs", runID)
	quarantineRoot := filepath.Join(canonicalRoot, legacyBriefQuarantineDirectory)
	legacyBriefRoot := filepath.Join(target, ".ai-team", "runs", runID, "brief")
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyBriefRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const name = "0001-intention.md"
	const content = "# intention\n"
	if err := os.WriteFile(filepath.Join(quarantineRoot, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(quarantineRoot, name), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(canonicalRoot, name), 0o444); err != nil {
		t.Fatal(err)
	}
	canonicalFD, err := openBriefDirectory(target, ".ai-team", "state", "briefs", runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBriefTreeAt(canonicalFD, canonicalRoot); err == nil {
		t.Fatal("canonical brief reader accepted a pending quarantine directory")
	}
	if err := closeBriefFD(canonicalFD); err != nil {
		t.Fatal(err)
	}

	if err := recoverLegacyBriefQuarantine(target, runID); err != nil {
		t.Fatalf("recover interrupted brief cleanup: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(legacyBriefRoot, name))
	if err != nil || string(got) != content {
		t.Fatalf("interrupted cleanup data was not restored: data=%q err=%v", got, err)
	}
	if _, err := readBriefTree(canonicalRoot); err != nil {
		t.Fatalf("canonical brief tree remained unreadable after recovery: %v", err)
	}
	if _, err := prepareControllerBriefRoot(target, runID); err != nil {
		t.Fatalf("startup preparation after quarantine recovery failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".ai-team", "runs", runID)); !os.IsNotExist(err) {
		t.Fatalf("legacy run root remains after recovered cleanup: err=%v", err)
	}
}

func TestPrepareControllerBriefRootRemovesOnlyEmptyOrphanLegacyRunRoot(t *testing.T) {
	t.Run("empty root from interrupted cleanup", func(t *testing.T) {
		target := t.TempDir()
		const runID = "empty-orphan-run-root"
		runRoot := filepath.Join(target, ".ai-team", "runs", runID)
		if err := os.MkdirAll(runRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareControllerBriefRoot(target, runID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
			t.Fatalf("empty orphan legacy run root remains: err=%v", err)
		}
	})

	t.Run("non-empty run evidence is preserved", func(t *testing.T) {
		target := t.TempDir()
		const runID = "non-empty-legacy-run-root"
		runRoot := filepath.Join(target, ".ai-team", "runs", runID)
		if err := os.MkdirAll(runRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		const evidence = "required run evidence"
		if err := os.WriteFile(filepath.Join(runRoot, "events.jsonl"), []byte(evidence), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareControllerBriefRoot(target, runID); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(runRoot, "events.jsonl"))
		if err != nil || string(got) != evidence {
			t.Fatalf("non-empty run evidence changed: data=%q err=%v", got, err)
		}
	})
}

func TestRecoverLegacyBriefDirectoryQuarantineRestoresEmptyDirectory(t *testing.T) {
	target := t.TempDir()
	const runID = "directory-quarantine-recovery"
	canonicalRoot := filepath.Join(target, ".ai-team", "state", "briefs", runID)
	quarantineRoot := filepath.Join(canonicalRoot, legacyBriefQuarantineDirectory)
	legacyRunRoot := filepath.Join(target, ".ai-team", "runs", runID)
	if err := os.MkdirAll(filepath.Join(quarantineRoot, legacyBriefDirectoryQuarantineEntry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyRunRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := recoverLegacyBriefQuarantine(target, runID); err != nil {
		t.Fatalf("recover interrupted directory cleanup: %v", err)
	}
	legacyBrief := filepath.Join(legacyRunRoot, "brief")
	if info, err := os.Stat(legacyBrief); err != nil || !info.IsDir() {
		t.Fatalf("quarantined empty brief directory was not restored: info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(quarantineRoot); !os.IsNotExist(err) {
		t.Fatalf("directory quarantine remains after recovery: err=%v", err)
	}
	if _, err := prepareControllerBriefRoot(target, runID); err != nil {
		t.Fatalf("startup cleanup after directory recovery failed: %v", err)
	}
	if _, err := os.Lstat(legacyRunRoot); !os.IsNotExist(err) {
		t.Fatalf("empty legacy run root remains after cleanup: err=%v", err)
	}
}

func TestRecoverLegacyBriefQuarantineFailsClosedOnUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, target, quarantineRoot string)
		check func(t *testing.T, quarantineRoot string)
	}{
		{
			name: "symlink file",
			setup: func(t *testing.T, target, quarantineRoot string) {
				t.Helper()
				outside := filepath.Join(target, "outside.md")
				if err := os.WriteFile(outside, []byte("outside data"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(quarantineRoot, "0001-intention.md")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			check: func(t *testing.T, quarantineRoot string) {
				t.Helper()
				info, err := os.Lstat(filepath.Join(quarantineRoot, "0001-intention.md"))
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("quarantined symlink was not preserved: info=%v err=%v", info, err)
				}
			},
		},
		{
			name: "hardlinked file",
			setup: func(t *testing.T, target, quarantineRoot string) {
				t.Helper()
				path := filepath.Join(quarantineRoot, "0001-intention.md")
				if err := os.WriteFile(path, []byte("linked data"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(path, filepath.Join(target, "outside-alias.md")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
			check: func(t *testing.T, quarantineRoot string) {
				t.Helper()
				if got, err := os.ReadFile(filepath.Join(quarantineRoot, "0001-intention.md")); err != nil || string(got) != "linked data" {
					t.Fatalf("quarantined hardlinked file was not preserved: data=%q err=%v", got, err)
				}
			},
		},
		{
			name: "non-empty directory marker",
			setup: func(t *testing.T, _, quarantineRoot string) {
				t.Helper()
				marker := filepath.Join(quarantineRoot, legacyBriefDirectoryQuarantineEntry)
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(marker, "user-data.txt"), []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, quarantineRoot string) {
				t.Helper()
				got, err := os.ReadFile(filepath.Join(quarantineRoot, legacyBriefDirectoryQuarantineEntry, "user-data.txt"))
				if err != nil || string(got) != "preserve" {
					t.Fatalf("quarantined non-empty directory was not preserved: data=%q err=%v", got, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			const runID = "unsafe-quarantine"
			quarantineRoot := filepath.Join(target, ".ai-team", "state", "briefs", runID, legacyBriefQuarantineDirectory)
			legacyRunRoot := filepath.Join(target, ".ai-team", "runs", runID)
			if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(legacyRunRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, target, quarantineRoot)
			if err := recoverLegacyBriefQuarantine(target, runID); err == nil {
				t.Fatal("unsafe quarantine entry was restored")
			}
			if _, err := os.Lstat(filepath.Join(legacyRunRoot, "brief")); !os.IsNotExist(err) {
				t.Fatalf("unsafe quarantine entry became worker-visible: err=%v", err)
			}
			tc.check(t, quarantineRoot)
		})
	}
}
