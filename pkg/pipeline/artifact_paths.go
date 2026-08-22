package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// artifact_paths.go — confinement путей артефактов внутри ArtifactRoot.

func validateRemovalPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("отказ удаления пути вне artifact root: %s", target)
	}
	current := rootAbs
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("отказ удаления через symbolic link: %s", current)
		}
	}
	return nil
}

func confinedArtifactPath(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	if full == rootAbs || !strings.HasPrefix(full, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q выходит за пределы %s", relative, rootAbs)
	}
	return full, nil
}

func validateExistingArtifactPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact path %s находится вне root %s", target, root)
	}
	current := rootAbs
	components := append([]string{""}, strings.Split(relative, string(filepath.Separator))...)
	for index := range components {
		if index > 0 {
			current = filepath.Join(current, components[index])
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact path %s проходит через symlink %s", target, current)
		}
	}
	info, err := os.Lstat(targetAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact path %s должен быть regular file или directory", target)
		}
		if info.Size() > maxArtifactFileBytes {
			return fmt.Errorf("artifact file %s слишком велик: %d > %d bytes", target, info.Size(), maxArtifactFileBytes)
		}
		return nil
	}
	var totalSize int64
	fileCount := 0
	return filepath.WalkDir(targetAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact directory %s содержит symlink %s", target, path)
		}
		relative, relErr := filepath.Rel(targetAbs, path)
		if relErr != nil {
			return relErr
		}
		if relative != "." && len(strings.Split(filepath.ToSlash(relative), "/")) > maxArtifactTreeDepth {
			return fmt.Errorf("artifact directory %s превышает max depth %d", target, maxArtifactTreeDepth)
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact directory %s содержит special file %s", target, path)
		}
		fileCount++
		totalSize += info.Size()
		if fileCount > maxArtifactTreeFiles || totalSize > maxArtifactTreeBytes {
			return fmt.Errorf("artifact directory %s превышает лимит files/bytes (%d/%d)", target, fileCount, totalSize)
		}
		return nil
	})
}

// collectInputs проверяет декларированные входы агента (stat) и добавляет
// loopback-входы; возвращает входы для промпта и полный список для отчёта.
