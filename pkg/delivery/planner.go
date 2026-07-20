package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

type Verification struct {
	SourceRunID         string
	WorkspaceDigest     string
	CheckEvidenceDigest string
	Preconditions       map[string]PreconditionEvidence
}

// BuildPlan builds a deterministic plan only from paths attributed to the
// current run. It never discovers and silently includes unrelated dirty files.
func BuildPlan(ctx context.Context, targetDir, feature, task string, attributedFiles []string, verification Verification) (Plan, error) {
	attributed := uniqueSorted(attributedFiles)
	if len(attributed) == 0 {
		return Plan{}, fmt.Errorf("delivery planner: нет атрибутированных files")
	}
	currentWorkspaceDigest, err := checks.WorkspaceDigest(targetDir)
	if err != nil {
		return Plan{}, fmt.Errorf("delivery planner: workspace digest: %w", err)
	}
	if verification.WorkspaceDigest != currentWorkspaceDigest {
		return Plan{}, fmt.Errorf("delivery planner: check verified workspace %s, current workspace is %s", verification.WorkspaceDigest, currentWorkspaceDigest)
	}
	current, err := commandOutput(ctx, targetDir, "git", "branch", "--show-current")
	if err != nil || current == "" {
		return Plan{}, fmt.Errorf("delivery planner: не удалось определить текущую git branch: %w", err)
	}
	base, err := detectBaseBranch(ctx, targetDir, current)
	if err != nil {
		return Plan{}, err
	}
	branch := "ai-team/" + feature
	if current != base && current != branch {
		return Plan{}, fmt.Errorf("delivery planner: текущая branch %q должна быть protected base %q или %q", current, base, branch)
	}
	baselineHead, err := commandOutput(ctx, targetDir, "git", "rev-parse", "--verify", base)
	if err != nil || baselineHead == "" {
		return Plan{}, fmt.Errorf("delivery planner: baseline HEAD для %s: %w", base, err)
	}
	currentHead, err := commandOutput(ctx, targetDir, "git", "rev-parse", "--verify", "HEAD")
	if err != nil || currentHead != baselineHead {
		return Plan{}, fmt.Errorf("delivery planner: branch %q содержит commits поверх baseline %s; требуется чистая ветка от base", current, baselineHead)
	}
	files, err := finalDeltaPaths(ctx, targetDir, baselineHead, attributed)
	if err != nil {
		return Plan{}, err
	}
	if len(files) == 0 {
		return Plan{}, fmt.Errorf("delivery planner: итоговый delta относительно baseline пуст")
	}
	fileDigests := make(map[string]string, len(files))
	fileModes := make(map[string]string, len(files))
	for _, file := range files {
		digest, digestErr := workspaceFileDigest(ctx, targetDir, baselineHead, file)
		if digestErr != nil {
			return Plan{}, digestErr
		}
		fileDigests[file] = digest
		mode, modeErr := workspaceFileMode(targetDir, file)
		if modeErr != nil {
			return Plan{}, modeErr
		}
		fileModes[file] = mode
	}

	description := compactWords(task, 9)
	if description == "" {
		description = "изменения workflow"
	}
	message := compactWords(feature+" "+description, 10)
	fileSummary := strings.Join(files, ", ")
	if utf8.RuneCountInString(fileSummary) > 220 {
		fileSummary = fmt.Sprintf("%d файлов", len(files))
	}
	body := fmt.Sprintf("Что изменено: %s.\nЗачем: %s.\nПроверка: обязательные проверки, review, tests и verification пройдены контроллером.", fileSummary, description)
	plan := Plan{
		SchemaVersion: SchemaVersion,
		Branch:        branch, BaseBranch: base, Remote: "origin", Files: files,
		FileDigests: fileDigests, FileModes: fileModes, BaselineHead: baselineHead,
		SourceRunID: verification.SourceRunID, VerifiedWorkspaceDigest: verification.WorkspaceDigest,
		CheckEvidenceDigest: verification.CheckEvidenceDigest,
		Preconditions:       clonePreconditions(verification.Preconditions),
		CommitMessage:       message, PRTitle: message, PRBody: body,
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func VerifyPreconditions(plan Plan, actual map[string]PreconditionEvidence) error {
	if len(plan.Preconditions) != len(actual) {
		return fmt.Errorf("delivery: precondition evidence set изменился")
	}
	for name, expected := range plan.Preconditions {
		if actual[name] != expected {
			return fmt.Errorf("delivery: precondition %s изменился после approved plan", name)
		}
	}
	return nil
}

func clonePreconditions(values map[string]PreconditionEvidence) map[string]PreconditionEvidence {
	result := make(map[string]PreconditionEvidence, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func finalDeltaPaths(ctx context.Context, targetDir, baselineHead string, candidates []string) ([]string, error) {
	seen := make(map[string]bool)
	diffArgs := append([]string{"diff", "--name-only", "-z", baselineHead, "--"}, candidates...)
	diff := exec.CommandContext(ctx, "git", diffArgs...)
	diff.Dir = targetDir
	diffOutput, err := diff.Output()
	if err != nil {
		return nil, fmt.Errorf("delivery planner: final tracked delta: %w", err)
	}
	for _, value := range strings.Split(string(diffOutput), "\x00") {
		if value != "" {
			seen[filepath.ToSlash(value)] = true
		}
	}
	untrackedArgs := append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, candidates...)
	untracked := exec.CommandContext(ctx, "git", untrackedArgs...)
	untracked.Dir = targetDir
	untrackedOutput, err := untracked.Output()
	if err != nil {
		return nil, fmt.Errorf("delivery planner: final untracked delta: %w", err)
	}
	for _, value := range strings.Split(string(untrackedOutput), "\x00") {
		if value != "" {
			seen[filepath.ToSlash(value)] = true
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func workspaceFileDigest(ctx context.Context, targetDir, baselineHead, relative string) (string, error) {
	fullPath := filepath.Join(targetDir, filepath.FromSlash(relative))
	info, err := os.Lstat(fullPath)
	if os.IsNotExist(err) {
		probe := exec.CommandContext(ctx, "git", "cat-file", "-e", baselineHead+":"+relative)
		probe.Dir = targetDir
		if probe.Run() != nil {
			return "", fmt.Errorf("delivery planner: attributed file %q отсутствует и не является deletion baseline", relative)
		}
		return DeletedDigest, nil
	}
	if err != nil {
		return "", fmt.Errorf("delivery planner: stat %q: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("delivery planner: file %q должен быть обычным файлом без symlink", relative)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func workspaceFileMode(targetDir, relative string) (string, error) {
	info, err := os.Lstat(filepath.Join(targetDir, filepath.FromSlash(relative)))
	if os.IsNotExist(err) {
		return DeletedMode, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("delivery planner: file %q должен быть обычным файлом без symlink", relative)
	}
	if info.Mode().Perm()&0111 != 0 {
		return "100755", nil
	}
	return "100644", nil
}

func VerifyPreparedWorkspace(targetDir string, plan Plan, workspaceDigest string) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if workspaceDigest != plan.VerifiedWorkspaceDigest {
		return fmt.Errorf("delivery: workspace bytes изменились после проверки: approved=%s current=%s", plan.VerifiedWorkspaceDigest, workspaceDigest)
	}
	for _, file := range plan.Files {
		fullPath := filepath.Join(targetDir, filepath.FromSlash(file))
		info, err := os.Lstat(fullPath)
		if os.IsNotExist(err) {
			if plan.FileDigests[file] != DeletedDigest {
				return fmt.Errorf("delivery: approved file %q отсутствует", file)
			}
			if plan.FileModes[file] != DeletedMode {
				return fmt.Errorf("delivery: approved deletion mode %q не совпадает", file)
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("delivery: approved file %q должен быть обычным файлом без symlink", file)
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if actual := hex.EncodeToString(digest[:]); actual != plan.FileDigests[file] {
			return fmt.Errorf("delivery: file %q изменился после проверки: approved=%s current=%s", file, plan.FileDigests[file], actual)
		}
		actualMode, modeErr := workspaceFileMode(targetDir, file)
		if modeErr != nil || actualMode != plan.FileModes[file] {
			return fmt.Errorf("delivery: mode file %q изменился после проверки: approved=%s current=%s", file, plan.FileModes[file], actualMode)
		}
	}
	return nil
}

func detectBaseBranch(ctx context.Context, targetDir, current string) (string, error) {
	if remoteHead, err := commandOutput(ctx, targetDir, "git", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if base := strings.TrimPrefix(remoteHead, "origin/"); base != "" {
			return base, nil
		}
	}
	for _, candidate := range []string{"main", "master"} {
		command := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+candidate)
		command.Dir = targetDir
		if command.Run() == nil {
			return candidate, nil
		}
	}
	if current == "main" || current == "master" {
		return current, nil
	}
	return "", fmt.Errorf("delivery planner: default branch не определена; настройте origin/HEAD или main/master")
}

func commandOutput(ctx context.Context, dir, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
		if value == "" || seen[value] || value == ".ai-team" || strings.HasPrefix(value, ".ai-team/") {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func compactWords(value string, limit int) string {
	words := strings.Fields(value)
	if len(words) > limit {
		words = words[:limit]
	}
	return strings.Join(words, " ")
}
