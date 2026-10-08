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

func closeControllerBriefRoot(rootFD int) error { return unix.Close(rootFD) }

func createInitialBriefAt(rootFD int, runID, intention string) (BriefDocument, error) {
	intention = strings.TrimSpace(intention)
	if intention == "" || len(intention) > maxBriefBytes {
		return BriefDocument{}, errors.New("business intention must contain 1..262144 bytes")
	}
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, err
	}
	defer unix.Close(runFD)
	content := []byte("# Исходное намерение\n\n" + intention + "\n")
	version, err := createBriefVersionAt(runFD, "0001-intention.md", content, "intention", "", "")
	return BriefDocument{Version: version, Content: content}, err
}

func appendBriefClarificationAt(rootFD int, runID, approvalID, questions, answer string) (BriefDocument, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return BriefDocument{}, errors.New("answer must contain 1..16384 bytes")
	}
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, errors.Join(errors.New("versioned business brief missing"), err)
	}
	defer unix.Close(runFD)
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

func listBriefStoreVersionsAt(rootFD int, runID string) ([]BriefVersion, error) {
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return nil, err
	}
	defer unix.Close(runFD)
	return listBriefVersionsAt(runFD)
}

func readBriefStoreDocumentAt(rootFD int, runID, versionID string) (BriefDocument, error) {
	runFD, err := openBriefDirectoryAt(rootFD, runID)
	if err != nil {
		return BriefDocument{}, err
	}
	defer unix.Close(runFD)
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
		_ = unix.Unlinkat(directoryFD, name, 0)
		return errors.Join(writeErr, closeErr)
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
		_ = unix.Close(fileFD)
		return nil, err
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG || after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 || after.Mode&0o222 != 0 {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("business brief file %q changed during safe open", name)
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
func secureMigrateLegacyBrief(targetDir, runID string, expected map[string][]byte) error {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer unix.Close(runsFD)
	runFD, err := openBriefDirectoryAt(runsFD, runID)
	if err != nil {
		return fmt.Errorf("open legacy run directory: %w", err)
	}
	defer unix.Close(runFD)
	briefFD, err := openBriefDirectoryAt(runFD, "brief")
	if err != nil {
		return fmt.Errorf("open legacy brief directory: %w", err)
	}
	_ = unix.Close(briefFD)
	briefsFD, err := openBriefDirectory(targetDir, ".ai-team", "state", "briefs")
	if err != nil {
		return fmt.Errorf("open canonical briefs directory: %w", err)
	}
	defer unix.Close(briefsFD)
	if err := renameBriefNoReplace(runFD, "brief", briefsFD, runID); err != nil {
		return err
	}
	// Confirm that the moved entry is a directory without following a raced
	// symlink. If the leaf was swapped immediately before rename, remove a
	// resulting symlink using unlinkat (which never follows it) and fail closed.
	movedFD, err := openBriefDirectoryAt(briefsFD, runID)
	if err != nil {
		var st unix.Stat_t
		if statErr := unix.Fstatat(briefsFD, runID, &st, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Unlinkat(briefsFD, runID, 0)
		}
		return fmt.Errorf("verify migrated brief directory: %w", err)
	}
	movedTree, readErr := readBriefTreeAt(movedFD, filepath.Join(targetDir, ".ai-team", "state", "briefs", runID))
	_ = unix.Close(movedFD)
	if readErr != nil || !equalBriefTrees(expected, movedTree) {
		rollbackErr := renameBriefNoReplace(briefsFD, runID, runFD, "brief")
		if readErr != nil {
			return fmt.Errorf("validate migrated brief contents: %w (rollback: %v)", readErr, rollbackErr)
		}
		return fmt.Errorf("legacy brief contents changed during migration (rollback: %v)", rollbackErr)
	}
	return nil
}

func secureRemoveLegacyBrief(targetDir, runID string, expected map[string][]byte) error {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer unix.Close(runsFD)
	runFD, err := openBriefDirectoryAt(runsFD, runID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy run directory: %w", err)
	}
	defer unix.Close(runFD)
	briefFD, err := openBriefDirectoryAt(runFD, "brief")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy brief directory: %w", err)
	}
	actual, err := readBriefTreeAt(briefFD, filepath.Join(targetDir, ".ai-team", "runs", runID, "brief"))
	if err != nil {
		_ = unix.Close(briefFD)
		return fmt.Errorf("revalidate legacy brief before cleanup: %w", err)
	}
	if !equalBriefTrees(expected, actual) {
		_ = unix.Close(briefFD)
		return fmt.Errorf("legacy brief contents changed before cleanup")
	}
	if err := removeBriefDirectoryContents(briefFD, expected); err != nil {
		_ = unix.Close(briefFD)
		return fmt.Errorf("remove legacy brief contents: %w", err)
	}
	_ = unix.Close(briefFD)
	if err := unix.Unlinkat(runFD, "brief", unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove legacy brief directory: %w", err)
	}
	return nil
}

func secureRemoveEmptyLegacyRunRoot(targetDir, runID string) error {
	runsFD, err := openBriefDirectory(targetDir, ".ai-team", "runs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open legacy runs directory: %w", err)
	}
	defer unix.Close(runsFD)
	err = unix.Unlinkat(runsFD, runID, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove empty legacy run directory: %w", err)
	}
	return nil
}

func secureRemoveEmptyCanonicalBrief(targetDir, runID string) error {
	briefsFD, err := openBriefDirectory(targetDir, ".ai-team", "state", "briefs")
	if err != nil {
		return err
	}
	defer unix.Close(briefsFD)
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
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = nextFD
	}
	return fd, nil
}

func openBriefDirectoryAt(parentFD int, name string) (int, error) {
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func removeBriefDirectoryContents(directoryFD int, expected map[string][]byte) error {
	dupFD, err := unix.Dup(directoryFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-directory")
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return err
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
		// unlinkat removes the validated filename relative to the pinned source
		// directory; it never follows a replaced symlink or an ancestor path.
		if err := unix.Unlinkat(directoryFD, entry.Name(), 0); err != nil {
			return err
		}
	}
	return nil
}

func readBriefTreeAt(directoryFD int, displayRoot string) (map[string][]byte, error) {
	dupFD, err := unix.Dup(directoryFD)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(dupFD), "brief-directory")
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return nil, err
	}
	tree := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		name := entry.Name()
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
		limit := int64(maxBriefBytes)
		if strings.HasSuffix(name, ".json") {
			limit = 1 << 20
		} else if !strings.HasSuffix(name, ".md") {
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
			_ = unix.Close(fileFD)
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("business brief file %q changed during safe open", filepath.Join(displayRoot, name))
		}
		file := os.NewFile(uintptr(fileFD), name)
		data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
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
