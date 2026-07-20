// Package safeio provides no-follow filesystem primitives for controller data.
package safeio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureDir creates a directory tree one component at a time and rejects any
// existing symlink or non-directory. Components must be single path elements.
func EnsureDir(root string, components ...string) (string, error) {
	current, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := requireDirectory(current); err != nil {
		return "", err
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component || strings.ContainsAny(component, `/\`) {
			return "", fmt.Errorf("unsafe directory component %q", component)
		}
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
			return "", err
		}
		if err := requireDirectory(current); err != nil {
			return "", err
		}
	}
	return current, nil
}

// ExistingDir validates an already existing directory chain without creating
// anything. It is used at CLI entry before reading project control data.
func ExistingDir(root string, components ...string) (string, error) {
	current, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := requireDirectory(current); err != nil {
		return "", err
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component || strings.ContainsAny(component, `/\`) {
			return "", fmt.Errorf("unsafe directory component %q", component)
		}
		current = filepath.Join(current, component)
		if err := requireDirectory(current); err != nil {
			return "", err
		}
	}
	return current, nil
}

// RejectSymlink rejects a final path if it already exists as a symlink or a
// special file. A missing file is valid because the caller may create it.
func RejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("controller file %s должен быть regular file без symlink", path)
	}
	return nil
}

// ValidateTree rejects symlinks and special files anywhere below an optional
// controller definition tree. A missing tree is valid.
func ValidateTree(path string) error {
	rootInfo, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("controller tree %s должен быть каталогом без symlink", path)
	}
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("controller tree %s содержит symlink %s", path, current)
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("controller tree %s содержит special file %s", path, current)
		}
		return nil
	})
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("controller path %s должен быть каталогом без symlink", path)
	}
	return nil
}
