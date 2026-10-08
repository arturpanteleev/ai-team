//go:build linux || darwin

package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	legacyBriefQuarantineDirectory      = ".legacy-quarantine"
	legacyBriefDirectoryQuarantineEntry = ".legacy-brief-directory-quarantine"
)

func closeControllerBriefRoot(rootFD int) error { return unix.Close(rootFD) }

func closeBriefFD(fd int) error {
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close controller brief file descriptor: %w", err)
	}
	return nil
}

func deferBriefFDClose(fd int, operation string, destination *error) {
	if err := closeBriefFD(fd); err != nil {
		*destination = errors.Join(*destination, fmt.Errorf("%s: %w", operation, err))
	}
}

func createInitialBriefAt(rootFD int, runID, intention string) (document BriefDocument, err error) {
	intention = strings.TrimSpace(intention)
	if intention == "" || len(intention) > maxBriefBytes {
		return BriefDocument{}, errors.New("business intention must contain 1..262144 bytes")
	}
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, err
	}
	defer deferBriefFDClose(runFD, "close run brief directory", &err)
	content := []byte("# Исходное намерение\n\n" + intention + "\n")
	version, err := createBriefVersionAt(runFD, "0001-intention.md", content, "intention", "", "")
	return BriefDocument{Version: version, Content: content}, err
}

func appendBriefClarificationAt(rootFD int, runID, approvalID, questions, answer string) (document BriefDocument, err error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return BriefDocument{}, errors.New("answer must contain 1..16384 bytes")
	}
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, errors.Join(errors.New("versioned business brief missing"), err)
	}
	defer deferBriefFDClose(runFD, "close run brief directory", &err)
	versions, err := listBriefVersionsAt(runFD)
	if err != nil || len(versions) == 0 {
		return BriefDocument{}, errors.Join(errors.New("versioned business brief missing"), err)
	}
	for _, existing := range versions {
		if strings.Contains(filepath.Base(existing.Path), "-answer-"+filepath.Base(approvalID)+".") {
			data, readErr := readBriefFileAt(runFD, filepath.Base(existing.Path), maxBriefBytes)
			if readErr != nil {
				return BriefDocument{}, readErr
			}
			existing.Kind, existing.ApprovalID = "clarification", approvalID
			existing.Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(existing.Path)))
			return BriefDocument{Version: existing, Content: data}, nil
		}
	}
	parent := versions[len(versions)-1]
	parentData, err := readBriefFileAt(runFD, filepath.Base(parent.Path), maxBriefBytes)
	if err != nil {
		return BriefDocument{}, err
	}
	name := fmt.Sprintf("%04d-answer-%s.md", len(versions)+1, filepath.Base(approvalID))
	addition := []byte(fmt.Sprintf("\n## Уточнение %d\n\n### Вопросы аналитика\n\n%s\n\n### Ответ Product Owner\n\n%s\n", len(versions), strings.TrimSpace(questions), answer))
	content := append(append([]byte(nil), parentData...), addition...)
	if len(content) > maxBriefBytes {
		return BriefDocument{}, errors.New("versioned business brief exceeds 262144 bytes")
	}
	version, err := createBriefVersionAt(runFD, name, content, "clarification", parent.ID, approvalID)
	return BriefDocument{Version: version, Content: content}, err
}

func listBriefStoreVersionsAt(rootFD int, runID string) (versions []BriefVersion, err error) {
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return nil, err
	}
	defer deferBriefFDClose(runFD, "close run brief directory", &err)
	return listBriefVersionsAt(runFD)
}

func readBriefStoreDocumentAt(rootFD int, runID, versionID string) (document BriefDocument, err error) {
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, err
	}
	defer deferBriefFDClose(runFD, "close run brief directory", &err)
	versions, err := listBriefVersionsAt(runFD)
	if err != nil {
		return BriefDocument{}, err
	}
	for _, version := range versions {
		if version.ID != versionID {
			continue
		}
		content, readErr := readBriefFileAt(runFD, filepath.Base(version.Path), maxBriefBytes)
		if readErr != nil {
			return BriefDocument{}, readErr
		}
		return BriefDocument{Version: version, Content: content}, nil
	}
	return BriefDocument{}, os.ErrNotExist
}

func listBriefVersionsAt(runFD int) ([]briefVersion, error) {
	tree, err := readBriefTreeAt(runFD, "pinned controller business brief directory")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tree))
	for name := range tree {
		if strings.HasSuffix(name, ".md") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	versions := make([]briefVersion, 0, len(names))
	for _, name := range names {
		content := tree[name]
		kind, parentID, approvalID := "intention", "", ""
		if len(versions) > 0 {
			kind = "clarification"
			parentID = versions[len(versions)-1].ID
			base := strings.TrimSuffix(name, filepath.Ext(name))
			if marker := strings.Index(base, "-answer-"); marker >= 0 {
				approvalID = base[marker+len("-answer-"):]
			}
		}
		version, err := createBriefVersionAt(runFD, name, content, kind, parentID, approvalID)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func createBriefVersionAt(runFD int, name string, content []byte, kind, parentID, approvalID string) (briefVersion, error) {
	if err := writeImmutableBriefAt(runFD, name, content); err != nil {
		return briefVersion{}, err
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	version := briefVersion{ID: "brief-" + hash[:16], Path: filepath.ToSlash(filepath.Join("brief", name)), SHA256: hash,
		ParentID: parentID, ApprovalID: approvalID, Kind: kind}
	metadata, err := json.MarshalIndent(struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}{ID: version.ID, Path: version.Path, SHA256: hash}, "", "  ")
	if err != nil {
		return briefVersion{}, err
	}
	if err := writeImmutableBriefAt(runFD, strings.TrimSuffix(name, filepath.Ext(name))+".json", append(metadata, '\n')); err != nil {
		return briefVersion{}, err
	}
	return version, nil
}

func writeImmutableBriefAt(directoryFD int, name string, data []byte) error {
	fileFD, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		existing, readErr := readBriefFileAt(directoryFD, name, maxBriefBytes)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("existing immutable brief version differs: %w", errors.Join(err, readErr))
		}
		return nil
	}
	file := os.NewFile(uintptr(fileFD), name)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil {
		writeErr = file.Chmod(0o444)
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cleanupErr := unix.Unlinkat(directoryFD, name, 0)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("remove incomplete immutable brief file %q: %w", name, cleanupErr)
		}
		return errors.Join(writeErr, closeErr, cleanupErr)
	}
	return nil
}

func readBriefFileAt(directoryFD int, name string, limit int64) ([]byte, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Mode&0o222 != 0 {
		return nil, fmt.Errorf("business brief file %q is not a private immutable regular file", name)
	}
	if before.Size < 0 || before.Size > limit {
		return nil, fmt.Errorf("business brief file %q exceeds limit %d", name, limit)
	}
	fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil {
		return nil, errors.Join(err, closeBriefFD(fileFD))
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG || after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 || after.Mode&0o222 != 0 {
		return nil, errors.Join(fmt.Errorf("business brief file %q changed during safe open", name), closeBriefFD(fileFD))
	}
	file := os.NewFile(uintptr(fileFD), name)
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("business brief file %q exceeds limit %d", name, limit)
	}
	return data, nil
}

// These mutations are anchored to directory descriptors opened one component
// at a time with O_NOFOLLOW. The workspace is writable by workers, so a path
// validated earlier must never be resolved again for a move or deletion.
func secureMigrateLegacyBrief(targetDir, runID string, expected map[string][]byte) (err error) {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer deferBriefFDClose(runsFD, "close legacy runs directory", &err)
	runFD, err := openBriefDirectoryAt(runsFD, runID)
	if err != nil {
		return fmt.Errorf("open legacy run directory: %w", err)
	}
	defer deferBriefFDClose(runFD, "close legacy run directory", &err)
	briefFD, err := openBriefDirectoryAt(runFD, "brief")
	if err != nil {
		return fmt.Errorf("open legacy brief directory: %w", err)
	}
	if err := closeBriefFD(briefFD); err != nil {
		return fmt.Errorf("close legacy brief directory before migration: %w", err)
	}
	briefsFD, err := openBriefDirectory(targetDir, ".ai-team", "state", "briefs")
	if err != nil {
		return fmt.Errorf("open canonical briefs directory: %w", err)
	}
	defer deferBriefFDClose(briefsFD, "close canonical briefs directory", &err)
	if err := renameBriefNoReplace(runFD, "brief", briefsFD, runID); err != nil {
		return err
	}
	// Confirm that the moved entry is a directory without following a raced
	// symlink. If the leaf was swapped immediately before rename, atomically
	// move the unexpected entry back to its source name. Never unlink the leaf:
	// it may be user data substituted after the earlier validation.
	movedFD, err := openBriefDirectoryAt(briefsFD, runID)
	if err != nil {
		return restoreMovedBriefAfterOpenFailure(runFD, briefsFD, runID, err)
	}
	movedTree, readErr := readBriefTreeAt(movedFD, filepath.Join(targetDir, ".ai-team", "state", "briefs", runID))
	closeErr := closeBriefFD(movedFD)
	if readErr != nil || !equalBriefTrees(expected, movedTree) {
		rollbackErr := renameBriefNoReplace(briefsFD, runID, runFD, "brief")
		if readErr != nil {
			return errors.Join(fmt.Errorf("validate migrated brief contents: %w", readErr), rollbackErr, closeErr)
		}
		return errors.Join(errors.New("legacy brief contents changed during migration"), rollbackErr, closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close migrated brief directory: %w", closeErr)
	}
	return nil
}

func secureRemoveLegacyBrief(targetDir, runID string, expected map[string][]byte) (err error) {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer deferBriefFDClose(runsFD, "close legacy runs directory", &err)
	runFD, err := openBriefDirectoryAt(runsFD, runID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy run directory: %w", err)
	}
	defer deferBriefFDClose(runFD, "close legacy run directory", &err)
	briefFD, err := openBriefDirectoryAt(runFD, "brief")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy brief directory: %w", err)
	}
	defer deferBriefFDClose(briefFD, "close legacy brief directory", &err)
	actual, err := readBriefTreeAt(briefFD, filepath.Join(targetDir, ".ai-team", "runs", runID, "brief"))
	if err != nil {
		return fmt.Errorf("revalidate legacy brief before cleanup: %w", err)
	}
	if !equalBriefTrees(expected, actual) {
		return fmt.Errorf("legacy brief contents changed before cleanup")
	}
	canonicalFD, quarantineFD, err := openOrCreateLegacyBriefQuarantine(targetDir, runID)
	if err != nil {
		return fmt.Errorf("open controller brief quarantine: %w", err)
	}
	defer func() {
		if quarantineFD >= 0 {
			deferBriefFDClose(quarantineFD, "close controller brief quarantine", &err)
		}
	}()
	defer deferBriefFDClose(canonicalFD, "close controller brief directory", &err)
	if err := removeBriefDirectoryContents(briefFD, quarantineFD, expected); err != nil {
		return fmt.Errorf("remove legacy brief contents: %w", err)
	}
	if err := removeEmptyLegacyBriefDirectory(runFD, quarantineFD, briefFD); err != nil {
		return fmt.Errorf("remove empty legacy brief directory: %w", err)
	}
	if err := closeBriefFD(quarantineFD); err != nil {
		quarantineFD = -1
		return fmt.Errorf("close controller brief quarantine: %w", err)
	}
	quarantineFD = -1
	if err := unix.Unlinkat(canonicalFD, legacyBriefQuarantineDirectory, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove empty controller brief quarantine: %w", err)
	}
	return nil
}

func secureRemoveEmptyLegacyRunRoot(targetDir, runID string) (err error) {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer deferBriefFDClose(runsFD, "close legacy runs directory", &err)
	err = unix.Unlinkat(runsFD, runID, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove empty legacy run directory: %w", err)
	}
	return nil
}

func secureRemoveEmptyCanonicalBrief(targetDir, runID string) (err error) {
	briefsFD, err := openBriefDirectory(targetDir, ".ai-team", "state", "briefs")
	if err != nil {
		return err
	}
	defer deferBriefFDClose(briefsFD, "close canonical briefs directory", &err)
	if err := unix.Unlinkat(briefsFD, runID, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return nil
}

func openBriefDirectory(root string, components ...string) (int, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return -1, err
	}
	fd, err := unix.Open(absolute, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range components {
		nextFD, openErr := openBriefDirectoryAt(fd, component)
		closeErr := closeBriefFD(fd)
		if openErr != nil {
			return -1, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return -1, errors.Join(closeErr, closeBriefFD(nextFD))
		}
		fd = nextFD
	}
	return fd, nil
}

func openBriefDirectoryAt(parentFD int, name string) (int, error) {
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func openBriefDirectoryFD(directoryFD int) (int, error) {
	// Opening "." relative to the pinned directory creates an independent
	// open file description; Dup would share the directory stream offset.
	return unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func restoreMovedBriefAfterOpenFailure(runFD, briefsFD int, runID string, cause error) error {
	rollbackErr := renameBriefNoReplace(briefsFD, runID, runFD, "brief")
	if rollbackErr != nil {
		rollbackErr = fmt.Errorf("restore moved brief entry to legacy location: %w", rollbackErr)
	}
	return errors.Join(fmt.Errorf("verify migrated brief directory: %w", cause), rollbackErr)
}

func removeEmptyLegacyBriefDirectory(runFD, quarantineFD, pinnedBriefFD int) error {
	if err := renameBriefNoReplace(runFD, "brief", quarantineFD, legacyBriefDirectoryQuarantineEntry); err != nil {
		return fmt.Errorf("quarantine legacy brief directory: %w", err)
	}
	movedFD, err := openBriefDirectoryAt(quarantineFD, legacyBriefDirectoryQuarantineEntry)
	if err != nil {
		return restoreQuarantinedLegacyBriefDirectory(runFD, quarantineFD, err)
	}
	var pinned, moved unix.Stat_t
	pinnedErr := unix.Fstat(pinnedBriefFD, &pinned)
	movedErr := unix.Fstat(movedFD, &moved)
	if pinnedErr != nil || movedErr != nil || pinned.Dev != moved.Dev || pinned.Ino != moved.Ino {
		closeErr := closeBriefFD(movedFD)
		cause := errors.Join(pinnedErr, movedErr)
		if cause == nil {
			cause = errors.New("legacy brief directory changed before cleanup")
		}
		return restoreQuarantinedLegacyBriefDirectory(runFD, quarantineFD, errors.Join(cause, closeErr))
	}
	empty, emptyErr := briefDirectoryIsEmpty(movedFD)
	closeErr := closeBriefFD(movedFD)
	if emptyErr != nil || closeErr != nil || !empty {
		cause := errors.Join(emptyErr, closeErr)
		if cause == nil {
			cause = errors.New("legacy brief directory is not empty after cleanup")
		}
		return restoreQuarantinedLegacyBriefDirectory(runFD, quarantineFD, cause)
	}
	if err := unix.Unlinkat(quarantineFD, legacyBriefDirectoryQuarantineEntry, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove verified quarantined legacy brief directory: %w", err)
	}
	return nil
}

func restoreQuarantinedLegacyBriefDirectory(runFD, quarantineFD int, cause error) error {
	rollbackErr := renameBriefNoReplace(quarantineFD, legacyBriefDirectoryQuarantineEntry, runFD, "brief")
	if rollbackErr != nil {
		rollbackErr = fmt.Errorf("restore quarantined legacy brief directory: %w", rollbackErr)
	}
	return errors.Join(cause, rollbackErr)
}

func briefDirectoryIsEmpty(directoryFD int) (bool, error) {
	dupFD, err := openBriefDirectoryFD(directoryFD)
	if err != nil {
		return false, err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-directory")
	entries, readErr := directory.ReadDir(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return false, closeErr
	}
	return len(entries) == 0, nil
}

func ensureBriefDirectoryAt(parentFD int, name string) (int, error) {
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	return openBriefDirectoryAt(parentFD, name)
}

func openOrCreateLegacyBriefQuarantine(targetDir, runID string) (canonicalFD, quarantineFD int, err error) {
	canonicalFD, err = openBriefDirectory(targetDir, ".ai-team", "state", "briefs", runID)
	if err != nil {
		return -1, -1, err
	}
	if quarantineFD, err = ensureBriefDirectoryAt(canonicalFD, legacyBriefQuarantineDirectory); err != nil {
		return -1, -1, errors.Join(err, closeBriefFD(canonicalFD))
	}
	return canonicalFD, quarantineFD, nil
}

func recoverLegacyBriefQuarantine(targetDir, runID string) (err error) {
	canonicalFD, err := openBriefDirectory(targetDir, ".ai-team", "state", "briefs", runID)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer deferBriefFDClose(canonicalFD, "close canonical brief directory during recovery", &err)
	quarantineFD, err := openBriefDirectoryAt(canonicalFD, legacyBriefQuarantineDirectory)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if quarantineFD >= 0 {
			deferBriefFDClose(quarantineFD, "close legacy brief quarantine during recovery", &err)
		}
	}()

	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if err != nil {
		return fmt.Errorf("open legacy runs directory for quarantine recovery: %w", err)
	}
	defer deferBriefFDClose(runsFD, "close legacy runs directory during recovery", &err)
	runFD, err := openBriefDirectoryAt(runsFD, runID)
	if err != nil {
		return fmt.Errorf("open legacy run directory for quarantine recovery: %w", err)
	}
	defer deferBriefFDClose(runFD, "close legacy run directory during recovery", &err)
	legacyBriefFD, err := openBriefDirectoryAt(runFD, "brief")
	if errors.Is(err, unix.ENOENT) {
		legacyBriefFD = -1
	} else if err != nil {
		return fmt.Errorf("open legacy brief directory for quarantine recovery: %w", err)
	}
	defer func() {
		if legacyBriefFD >= 0 {
			deferBriefFDClose(legacyBriefFD, "close legacy brief directory during recovery", &err)
		}
	}()

	dupFD, err := openBriefDirectoryFD(quarantineFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-quarantine")
	entries, err := directory.ReadDir(-1)
	if closeErr := directory.Close(); err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	for _, entry := range entries {
		if entry.Name() == legacyBriefDirectoryQuarantineEntry && len(entries) != 1 {
			return errors.New("interrupted quarantine mixes a legacy brief directory with file entries")
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == legacyBriefDirectoryQuarantineEntry {
			if legacyBriefFD >= 0 {
				return errors.New("legacy brief path exists while a quarantined directory awaits recovery")
			}
			movedFD, err := openBriefDirectoryAt(quarantineFD, name)
			if err != nil {
				return fmt.Errorf("validate quarantined legacy brief directory: %w", err)
			}
			empty, emptyErr := briefDirectoryIsEmpty(movedFD)
			closeErr := closeBriefFD(movedFD)
			if emptyErr != nil || closeErr != nil || !empty {
				cause := errors.Join(emptyErr, closeErr)
				if cause == nil {
					cause = errors.New("quarantined legacy brief directory is not empty")
				}
				return cause
			}
			if err := renameBriefNoReplace(quarantineFD, name, runFD, "brief"); err != nil {
				return fmt.Errorf("restore interrupted legacy brief directory: %w", err)
			}
			continue
		}
		limit, supported := briefFileLimit(name)
		if !supported {
			return fmt.Errorf("unsupported entry %q in interrupted legacy brief quarantine", name)
		}
		if _, err := readBriefFileAt(quarantineFD, name, limit); err != nil {
			return fmt.Errorf("validate quarantined legacy brief %q: %w", name, err)
		}
		if legacyBriefFD < 0 {
			legacyBriefFD, err = ensureBriefDirectoryAt(runFD, "brief")
			if err != nil {
				return fmt.Errorf("create legacy brief directory for quarantine recovery: %w", err)
			}
		}
		if err := renameBriefNoReplace(quarantineFD, name, legacyBriefFD, name); err != nil {
			return fmt.Errorf("restore quarantined legacy brief %q: %w", name, err)
		}
	}
	if err := closeBriefFD(quarantineFD); err != nil {
		quarantineFD = -1
		return fmt.Errorf("close recovered legacy brief quarantine: %w", err)
	}
	quarantineFD = -1
	if err := unix.Unlinkat(canonicalFD, legacyBriefQuarantineDirectory, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove recovered legacy brief quarantine: %w", err)
	}
	return nil
}

func briefFileLimit(name string) (int64, bool) {
	if strings.HasSuffix(name, ".md") {
		return maxBriefBytes, true
	}
	if strings.HasSuffix(name, ".json") {
		return 1 << 20, true
	}
	return 0, false
}

func removeBriefDirectoryContents(directoryFD, quarantineFD int, expected map[string][]byte) error {
	dupFD, err := openBriefDirectoryFD(directoryFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-directory")
	entries, err := directory.ReadDir(-1)
	if closeErr := directory.Close(); err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("legacy brief directory changed before cleanup")
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return fmt.Errorf("unexpected entry %q in legacy brief directory cleanup", entry.Name())
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			return fmt.Errorf("legacy brief file %q changed before cleanup", entry.Name())
		}
		name := entry.Name()
		if err := renameBriefNoReplace(directoryFD, name, quarantineFD, name); err != nil {
			return fmt.Errorf("quarantine legacy brief file %q: %w", name, err)
		}
		limit, supported := briefFileLimit(name)
		if !supported {
			return fmt.Errorf("unsupported legacy brief file %q during cleanup", name)
		}
		moved, readErr := readBriefFileAt(quarantineFD, name, limit)
		if readErr != nil || !bytes.Equal(moved, expected[name]) {
			restoreErr := renameBriefNoReplace(quarantineFD, name, directoryFD, name)
			if restoreErr != nil {
				restoreErr = fmt.Errorf("restore changed legacy brief file %q: %w", name, restoreErr)
			}
			changedErr := fmt.Errorf("legacy brief file %q changed before cleanup", name)
			if readErr != nil {
				changedErr = errors.Join(changedErr, readErr)
			}
			return errors.Join(changedErr, restoreErr)
		}
		if err := unix.Unlinkat(quarantineFD, name, 0); err != nil {
			return fmt.Errorf("remove verified quarantined legacy brief file %q: %w", name, err)
		}
	}
	return nil
}

func readBriefTreeAt(directoryFD int, displayRoot string) (map[string][]byte, error) {
	dupFD, err := openBriefDirectoryFD(directoryFD)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-directory")
	entries, err := directory.ReadDir(-1)
	if closeErr := directory.Close(); err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	tree := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == legacyBriefQuarantineDirectory {
			return nil, errors.New("controller brief contains an unfinished legacy quarantine; restore it before reading")
		}
		var before unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o222 != 0 {
			return nil, fmt.Errorf("business brief entry %q must be a regular file", filepath.Join(displayRoot, name))
		}
		if before.Nlink != 1 {
			return nil, fmt.Errorf("business brief file %q has %d hard links; refusing migration", filepath.Join(displayRoot, name), before.Nlink)
		}
		limit, supported := briefFileLimit(name)
		if !supported {
			return nil, fmt.Errorf("business brief contains unsupported file %q", name)
		}
		if before.Size > limit {
			return nil, fmt.Errorf("business brief file %q exceeds limit %d", filepath.Join(displayRoot, name), limit)
		}
		fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		var after unix.Stat_t
		statErr := unix.Fstat(fileFD, &after)
		if statErr != nil || after.Mode&unix.S_IFMT != unix.S_IFREG || after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 {
			closeErr := closeBriefFD(fileFD)
			if statErr != nil {
				return nil, errors.Join(statErr, closeErr)
			}
			return nil, errors.Join(fmt.Errorf("business brief file %q changed during safe open", filepath.Join(displayRoot, name)), closeErr)
		}
		file := os.NewFile(uintptr(fileFD), name)
		data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("business brief file %q exceeds limit %d", filepath.Join(displayRoot, name), limit)
		}
		tree[name] = data
	}
	if err := validateBriefTreeData(displayRoot, tree); err != nil {
		return nil, err
	}
	return tree, nil
}
