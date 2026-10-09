package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const inputScopeFileLimit = int64(64 << 20)

// prepareInputOnlyWorkspace gives an inputs-only agent a private, minimal
// working tree. The harness sees immutable copies of declared inputs and can
// write only staged output paths; after success, the controller copies those
// outputs back to the real artifact root. Adapters still enforce their own
// read/tool permissions, and unsupported adapters fail closed through the
// CapInputScopedRead launch requirement.
func prepareInputOnlyWorkspace(agent *Agent, task *Task, inputs []Artifact) (*Task, []Artifact, func() error, func(), error) {
	noPublish := func() error { return nil }
	noCleanup := func() {}
	if agent == nil || agent.ReadScope != ReadScopeInputsOnly {
		return task, inputs, noPublish, noCleanup, nil
	}
	if task == nil {
		return nil, nil, nil, noCleanup, fmt.Errorf("task обязателен")
	}
	root, err := os.MkdirTemp("", "ai-team-input-only-*")
	if err != nil {
		return nil, nil, nil, noCleanup, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanup()
		return nil, nil, nil, noCleanup, err
	}

	scopedInputs := make([]Artifact, 0, len(inputs))
	for index, input := range inputs {
		sourceInfo, statErr := os.Lstat(input.Path)
		if statErr != nil {
			cleanup()
			return nil, nil, nil, noCleanup, fmt.Errorf("input %q: %w", input.Name, statErr)
		}
		inputRoot := filepath.Join(root, "declared-inputs", fmt.Sprintf("%03d", index))
		if err := os.MkdirAll(inputRoot, 0o700); err != nil {
			cleanup()
			return nil, nil, nil, noCleanup, err
		}
		copyPath := inputRoot
		if !sourceInfo.IsDir() {
			base := filepath.Base(filepath.Clean(input.Path))
			if base == "." || base == string(filepath.Separator) || base == "" {
				base = "artifact"
			}
			copyPath = filepath.Join(inputRoot, base)
		}
		if err := copyInputTree(input.Path, copyPath); err != nil {
			cleanup()
			return nil, nil, nil, noCleanup, fmt.Errorf("input %q: %w", input.Name, err)
		}
		input.Path = copyPath
		scopedInputs = append(scopedInputs, input)
	}

	scopedTask := *task
	scopedTask.TargetDir = root
	scopedTask.ArtifactRoot = root
	if _, err := scopedArtifactPath(root, filepath.Join(task.Feature, "placeholder")); err != nil {
		cleanup()
		return nil, nil, nil, noCleanup, fmt.Errorf("feature path: %w", err)
	}
	for _, rel := range scopedOutputPaths(agent, task) {
		full, pathErr := scopedArtifactPath(root, rel)
		if pathErr != nil {
			cleanup()
			return nil, nil, nil, noCleanup, pathErr
		}
		if err := safeio.EnsureDirPath(filepath.Dir(full)); err != nil {
			cleanup()
			return nil, nil, nil, noCleanup, err
		}
	}

	publish := func() error {
		for _, rel := range scopedOutputPaths(agent, task) {
			source, sourceErr := scopedArtifactPath(root, rel)
			if sourceErr != nil {
				return sourceErr
			}
			destination, destinationErr := scopedArtifactPath(task.ArtifactRoot, rel)
			if destinationErr != nil {
				return destinationErr
			}
			if err := copyOutputTree(source, destination); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
		return nil
	}
	return &scopedTask, scopedInputs, publish, cleanup, nil
}

func scopedOutputPaths(agent *Agent, task *Task) []string {
	paths := make([]string, 0, len(agent.Outputs)+2)
	for _, output := range agent.Outputs {
		paths = append(paths, ReplaceVars(output, task.Feature))
	}
	paths = append(paths,
		filepath.Join(task.Feature, "status", agent.Name+".md"),
		filepath.Join(task.Feature, ".stage-summary", agent.Name+".md"),
	)
	return paths
}

func scopedArtifactPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("output path %q должен быть относительным", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path %q выходит за пределы artifact root", relative)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path %q выходит за пределы artifact root", relative)
	}
	return full, nil
}

func copyInputTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink input запрещён")
	}
	if info.Mode().IsRegular() {
		data, err := safeio.ReadRegularFile(source, inputScopeFileLimit)
		if err != nil {
			return err
		}
		return safeio.WriteRegularFileNoFollow(destination, data, 0o444)
	}
	if !info.IsDir() {
		return fmt.Errorf("input должен быть обычным файлом или каталогом")
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(destination, rel)
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink input %s запрещён", rel)
		}
		switch {
		case entryInfo.IsDir():
			return os.MkdirAll(to, 0o700)
		case entryInfo.Mode().IsRegular():
			data, err := safeio.ReadRegularFile(path, inputScopeFileLimit)
			if err != nil {
				return err
			}
			return safeio.WriteRegularFileNoFollow(to, data, 0o444)
		default:
			return fmt.Errorf("special input %s запрещён", rel)
		}
	})
}

func copyOutputTree(source, destination string) error {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil // controller's normal missing-output validation remains authoritative.
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink output запрещён")
	}
	if info.Mode().IsRegular() {
		return copyOutputFile(source, destination)
	}
	if !info.IsDir() {
		return fmt.Errorf("output должен быть обычным файлом или каталогом")
	}
	if err := safeio.EnsureDirPath(destination); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(destination, rel)
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink output %s запрещён", rel)
		}
		switch {
		case entryInfo.IsDir():
			return safeio.EnsureDirPath(to)
		case entryInfo.Mode().IsRegular():
			return copyOutputFile(path, to)
		default:
			return fmt.Errorf("special output %s запрещён", rel)
		}
	})
}

func copyOutputFile(source, destination string) error {
	data, err := safeio.ReadRegularFile(source, inputScopeFileLimit)
	if err != nil {
		return err
	}
	return safeio.WriteRegularFileNoFollow(destination, data, 0o644)
}
