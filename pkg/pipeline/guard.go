package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/scope"
)

// guard.go — mutation guard: сравнение снапшотов до/после этапа.

func (rs *runState) enforceMutationGuard(
	a *agent.Agent,
	name string,
	workspaceBefore filesystemSnapshot,
	controlBefore filesystemSnapshot,
	gitBefore gitMetadataSnapshot,
	gitAvailable, guardWorkspace bool,
	artifactBefore filesystemSnapshot,
	result *notifier.StageResult,
) error {
	var guardErrors []error
	if guardWorkspace {
		workspaceAfter, err := captureWorkspaceSnapshot(rs.sourceDir())
		if err != nil {
			guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить workspace state: %w", name, err))
		} else {
			changedPaths := changedSnapshotPaths(workspaceBefore, workspaceAfter)
			result.Mutations = append([]string(nil), changedPaths...)
			result.MutationChanges = classifyMutationChanges(workspaceBefore, workspaceAfter, changedPaths)
			if a.Mutation == "none" && workspaceBefore.Fingerprint != workspaceAfter.Fingerprint {
				// Пути в сообщении — не косметика: без них нарушитель ищется
				// только в манифесте, а именно эти пути раньше терялись (QS-03).
				guardErrors = append(guardErrors, fmt.Errorf(
					"агент %s нарушил mutation policy: read-only этап изменил проект: %s",
					name, summarizePaths(changedPaths)))
			}
			if a.Mutation == "source" || a.Mutation == "tests" {
				var denied []string
				for _, changedPath := range changedPaths {
					if !scope.MatchAny(a.AllowedPaths, changedPath) {
						denied = append(denied, changedPath)
					}
				}
				if len(denied) > 0 {
					guardErrors = append(guardErrors, fmt.Errorf("агент %s нарушил mutation policy: пути вне allowed_paths: %s", name, strings.Join(denied, ", ")))
				}
				var dirtyTouched []string
				for _, changedPath := range changedPaths {
					if rs.userOwnedPaths[changedPath] {
						dirtyTouched = append(dirtyTouched, changedPath)
					}
				}
				if len(dirtyTouched) > 0 {
					guardErrors = append(guardErrors, fmt.Errorf("агент %s изменил user-owned файлы, существовавшие до run: %s", name, strings.Join(dirtyTouched, ", ")))
				}
			}
			if a.RequireDiff && len(changedPaths) == 0 {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s не создал изменений в коде", name))
			}
		}
		// Controller-owned `.ai-team` вне artifact namespace: config.yaml,
		// локальные def'ы агентов, любой файл, положенный «мимо» контракта.
		// Никакая mutation policy этого не разрешает, поэтому проверка общая.
		controlAfter, controlErr := captureControlMetadataSnapshot(rs.sourceDir())
		if controlErr != nil {
			guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить control metadata state: %w", name, controlErr))
		} else if controlChanged := changedSnapshotPaths(controlBefore, controlAfter); len(controlChanged) > 0 {
			changes := classifyMutationChanges(controlBefore, controlAfter, controlChanged)
			prefixed := make([]string, 0, len(controlChanged))
			for i, changedPath := range controlChanged {
				prefixed = append(prefixed, ".ai-team/"+changedPath)
				changes[i].Path = prefixed[i]
			}
			result.Mutations = append(result.Mutations, prefixed...)
			result.MutationChanges = append(result.MutationChanges, changes...)
			guardErrors = append(guardErrors, fmt.Errorf(
				"агент %s нарушил mutation policy: изменил controller-owned метаданные: %s",
				name, strings.Join(prefixed, ", ")))
		}
		if gitAvailable {
			gitAfter, stillAvailable, err := captureGitMetadataSnapshot(rs.sourceDir())
			if err != nil || !stillAvailable {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить git metadata state: %w", name, err))
			} else if gitBefore.Fingerprint != gitAfter.Fingerprint {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s нарушил mutation policy: изменил HEAD, branch или git index", name))
			}
		}
	}

	artifactAfter, err := captureArtifactSnapshot(rs.task.ArtifactRoot)
	if err != nil {
		guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить artifact state: %w", name, err))
	} else {
		var denied []string
		for _, changedPath := range changedSnapshotPaths(artifactBefore, artifactAfter) {
			fullPath := filepath.Join(rs.task.ArtifactRoot, filepath.FromSlash(changedPath))
			info, statErr := os.Lstat(fullPath)
			unsafeLink := statErr == nil && info.Mode()&os.ModeSymlink != 0
			if unsafeLink || !rs.artifactMutationAllowed(a, name, changedPath) {
				denied = append(denied, changedPath)
			}
		}
		if len(denied) > 0 {
			guardErrors = append(guardErrors, fmt.Errorf("агент %s изменил undeclared artifacts: %s", name, strings.Join(denied, ", ")))
		}
	}
	return errors.Join(guardErrors...)
}

// summarizePaths — ограниченный по длине список путей для текста ошибки:
// нарушение read-only может затронуть весь node_modules, и вываливать его в
// консоль целиком бессмысленно.
func summarizePaths(paths []string) string {
	const limit = 12
	if len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s и ещё %d", strings.Join(paths[:limit], ", "), len(paths)-limit)
}

func (rs *runState) artifactMutationAllowed(a *agent.Agent, name, relative string) bool {
	relative = filepath.ToSlash(relative)
	allowedFiles := []string{
		filepath.ToSlash(filepath.Join(rs.runCfg.Feature, "status", name+".md")),
		filepath.ToSlash(filepath.Join(rs.runCfg.Feature, ".stage-summary", name+".md")),
	}
	for _, allowed := range allowedFiles {
		if relative == allowed {
			return true
		}
	}
	for _, outputPath := range a.Outputs {
		output := filepath.ToSlash(filepath.Clean(filepath.FromSlash(runtime.ReplaceVars(outputPath, rs.runCfg.Feature))))
		if relative == output {
			return true
		}
		fullOutput := filepath.Join(rs.task.ArtifactRoot, filepath.FromSlash(output))
		if info, err := os.Lstat(fullOutput); err == nil && info.IsDir() && strings.HasPrefix(relative, output+"/") {
			return true
		}
	}
	return false
}
