package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// ArtifactDigestAt hashes an artifact below root while refusing symbolic links
// in every path component. Each component is opened relative to an os.Root and
// checked against the opened handle, so replacing a checked path cannot redirect
// the read outside the held directory tree.
func ArtifactDigestAt(root, rel string) (artifactType string, size int64, digest string, err error) {
	return artifactAt(root, rel, "", false)
}

// CopyArtifactAt copies an artifact below root using the same rooted no-follow
// traversal as ArtifactDigestAt. The returned digest covers the bytes written
// to destination.
func CopyArtifactAt(root, rel, destination string) (artifactType string, size int64, digest string, err error) {
	if destination == "" {
		return "", 0, "", errors.New("artifact destination is required")
	}
	return artifactAt(root, rel, destination, true)
}

type rootedArtifact struct {
	dir  *os.Root
	file *os.File
	info os.FileInfo
	// observed is the parent directory entry identity from before opening.
	observed os.FileInfo
}

func artifactAt(root, rel, destination string, copyOutput bool) (string, int64, string, error) {
	components, err := artifactPathComponents(rel)
	if err != nil {
		return "", 0, "", err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", 0, "", err
	}
	defer func() { _ = rootHandle.Close() }()

	entry, err := openRootedArtifact(rootHandle, components)
	if err != nil {
		return "", 0, "", err
	}
	if entry.dir != nil {
		defer func() { _ = entry.dir.Close() }()
		if copyOutput {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return "", 0, "", err
			}
		}
		h := sha256.New()
		size, err := digestRootedDirectory(entry.dir, "", destination, copyOutput, h)
		if err != nil {
			return "", 0, "", err
		}
		return "directory", size, hex.EncodeToString(h.Sum(nil)), nil
	}
	defer func() { _ = entry.file.Close() }()
	if !entry.info.Mode().IsRegular() {
		return "", 0, "", fmt.Errorf("artifact %s has unsupported type", rel)
	}
	size, digest, err := digestRootedFile(entry.file, destination, copyOutput)
	if err != nil {
		return "", 0, "", err
	}
	return "file", size, digest, nil
}

func artifactPathComponents(rel string) ([]string, error) {
	converted := filepath.FromSlash(rel)
	if rel == "" || strings.IndexByte(rel, 0) >= 0 || filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" ||
		(filepath.Separator == '\\' && strings.Contains(rel, "\\")) {
		return nil, fmt.Errorf("artifact path %q must be relative and slash-separated", rel)
	}
	components := strings.Split(rel, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("artifact path %q is not canonical", rel)
		}
	}
	return components, nil
}

func openRootedArtifact(root *os.Root, components []string) (rootedArtifact, error) {
	current := root
	ownedRoot := false
	for index, component := range components {
		last := index == len(components)-1
		entry, err := openRootedChild(current, component, last)
		if err != nil {
			if ownedRoot {
				_ = current.Close()
			}
			return rootedArtifact{}, err
		}
		if entry.file != nil {
			if ownedRoot {
				_ = current.Close()
			}
			return entry, nil
		}
		if last {
			if ownedRoot {
				_ = current.Close()
			}
			return entry, nil
		}
		if ownedRoot {
			_ = current.Close()
		}
		current = entry.dir
		ownedRoot = true
	}
	if ownedRoot {
		_ = current.Close()
	}
	return rootedArtifact{}, fmt.Errorf("empty artifact path")
}

func openRootedChild(parent *os.Root, name string, final bool) (rootedArtifact, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return rootedArtifact{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return rootedArtifact{}, fmt.Errorf("symbolic link %s is not allowed in artifact path", name)
	}
	if before.IsDir() {
		child, err := parent.OpenRoot(name)
		if err != nil {
			return rootedArtifact{}, err
		}
		opened, statErr := child.Stat(".")
		if statErr != nil {
			_ = child.Close()
			return rootedArtifact{}, statErr
		}
		after, statErr := parent.Lstat(name)
		if statErr != nil {
			_ = child.Close()
			return rootedArtifact{}, statErr
		}
		if err := checkOpenedIdentity(name, before, opened, after, true); err != nil {
			_ = child.Close()
			return rootedArtifact{}, err
		}
		return rootedArtifact{dir: child, info: opened, observed: before}, nil
	}
	if !final {
		return rootedArtifact{}, fmt.Errorf("artifact path component %s is not a directory", name)
	}
	if !before.Mode().IsRegular() {
		return rootedArtifact{}, fmt.Errorf("artifact %s has unsupported type", name)
	}
	file, err := parent.Open(name)
	if err != nil {
		return rootedArtifact{}, err
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return rootedArtifact{}, statErr
	}
	after, statErr := parent.Lstat(name)
	if statErr != nil {
		_ = file.Close()
		return rootedArtifact{}, statErr
	}
	if err := checkOpenedIdentity(name, before, opened, after, false); err != nil {
		_ = file.Close()
		return rootedArtifact{}, err
	}
	return rootedArtifact{file: file, info: opened, observed: before}, nil
}

func checkOpenedIdentity(name string, before, opened, after os.FileInfo, directory bool) error {
	if before.Mode()&os.ModeSymlink != 0 || after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, opened) || !os.SameFile(after, opened) ||
		before.IsDir() != directory || opened.IsDir() != directory || after.IsDir() != directory {
		return fmt.Errorf("artifact path component %s changed while opening", name)
	}
	if !directory && (!before.Mode().IsRegular() || !opened.Mode().IsRegular() || !after.Mode().IsRegular()) {
		return fmt.Errorf("artifact %s is not a regular file", name)
	}
	return nil
}

func digestRootedDirectory(dir *os.Root, rel, destination string, copyOutput bool, h hash.Hash) (int64, error) {
	names, err := rootedNames(dir)
	if err != nil {
		return 0, err
	}
	var size int64
	observed := make(map[string]os.FileInfo, len(names))
	for _, name := range names {
		entry, err := openRootedChild(dir, name, true)
		if err != nil {
			return 0, err
		}
		observed[name] = entry.observed
		childRel := name
		if rel != "" {
			childRel = path.Join(rel, name)
		}
		childDestination := ""
		if copyOutput {
			childDestination = filepath.Join(destination, filepath.FromSlash(name))
		}
		if entry.dir != nil {
			if copyOutput {
				if err := os.MkdirAll(childDestination, 0755); err != nil {
					_ = entry.dir.Close()
					return 0, err
				}
			}
			childSize, err := digestRootedDirectory(entry.dir, childRel, childDestination, copyOutput, h)
			_ = entry.dir.Close()
			if err != nil {
				return 0, err
			}
			size += childSize
		} else {
			fileSize, fileDigest, err := digestRootedFile(entry.file, childDestination, copyOutput)
			_ = entry.file.Close()
			if err != nil {
				return 0, err
			}
			_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00", filepath.ToSlash(filepath.FromSlash(childRel)), fileSize, fileDigest)
			size += fileSize
		}
	}
	for _, name := range names {
		current, err := dir.Lstat(name)
		if err != nil {
			return 0, err
		}
		if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(observed[name], current) {
			return 0, fmt.Errorf("artifact directory entry %s changed while reading", name)
		}
	}
	afterNames, err := rootedNames(dir)
	if err != nil {
		return 0, err
	}
	if !sameNames(names, afterNames) {
		return 0, fmt.Errorf("artifact directory changed while reading")
	}
	return size, nil
}

func rootedNames(dir *os.Root) ([]string, error) {
	file, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	names, readErr := file.Readdirnames(-1)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func sameNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func digestRootedFile(source *os.File, destination string, copyOutput bool) (int64, string, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	h := sha256.New()
	var writer io.Writer = h
	var output *os.File
	if copyOutput {
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return 0, "", err
		}
		var err error
		output, err = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, safeio.ReadOnlyFileMode)
		if err != nil {
			return 0, "", err
		}
		writer = io.MultiWriter(h, output)
	}
	size, copyErr := io.Copy(writer, source)
	if output != nil {
		closeErr := output.Close()
		copyErr = errors.Join(copyErr, closeErr)
		if copyErr != nil {
			_ = os.Remove(destination)
		}
	}
	if copyErr != nil {
		return 0, "", copyErr
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}
