package candidate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateAndLoadKeepsLiveCheckoutUnchanged(t *testing.T) {
	target := gitRepository(t)
	liveHead := command(t, target, "rev-parse", "HEAD")
	manager, available, err := Create(context.Background(), target, "run-1")
	if err != nil || !available {
		t.Fatalf("create: available=%t err=%v", available, err)
	}
	if err := os.WriteFile(filepath.Join(manager.Root(), "candidate.txt"), []byte("candidate"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "candidate.txt")); !os.IsNotExist(err) {
		t.Fatalf("candidate mutation появилась в live checkout: %v", err)
	}
	if command(t, target, "rev-parse", "HEAD") != liveHead {
		t.Fatal("live HEAD изменился")
	}
	loaded, err := Load(context.Background(), target, "run-1")
	if err != nil || loaded.Root() != manager.Root() {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	identity, err := loaded.Identity()
	if err != nil || identity.WorkspaceSHA256 == "" || identity.BaseCommit != liveHead {
		t.Fatalf("identity: %+v %v", identity, err)
	}
}

func TestManagerRejectsSymlinkAsFinalTargetComponent(t *testing.T) {
	target := gitRepository(t)
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := Create(context.Background(), link, "final-link-create"); err == nil {
		t.Fatal("Create accepted a symlink as the final target component")
	}
	manager, available, err := Create(context.Background(), target, "final-link-load")
	if err != nil || !available || manager == nil {
		t.Fatalf("create canonical target: available=%v manager=%v err=%v", available, manager, err)
	}
	if _, err := Load(context.Background(), link, "final-link-load"); err == nil {
		t.Fatal("Load accepted a symlink as the final target component")
	}
}

type recordingMetadataStore struct {
	FileMetadataStore
	creates, reads int
}

func (s *recordingMetadataStore) Create(m Metadata) error {
	s.creates++
	return s.FileMetadataStore.Create(m)
}
func (s *recordingMetadataStore) Read(target, runID string) (Metadata, error) {
	s.reads++
	return s.FileMetadataStore.Read(target, runID)
}

func TestCreateAndResumeUseInjectedMetadataStore(t *testing.T) {
	target := gitRepository(t)
	store := &recordingMetadataStore{}
	manager, available, err := CreateWithMetadataStore(context.Background(), target, "api-run", store)
	if err != nil || !available || manager == nil || store.creates != 1 {
		t.Fatalf("create: available=%v manager=%v calls=%+v err=%v", available, manager, store, err)
	}
	loaded, err := LoadWithMetadataStore(context.Background(), target, "api-run", store)
	if err != nil || loaded.Root() != manager.Root() || store.reads != 1 {
		t.Fatalf("load through store: manager=%v calls=%+v err=%v", loaded, store, err)
	}
}

func TestMetadataStoreRejectsConflictingIdentityAndTraversal(t *testing.T) {
	target := gitRepository(t)
	manager, available, err := Create(context.Background(), target, "bound-run")
	if err != nil || !available {
		t.Fatal(err)
	}
	metadata := manager.Metadata()
	metadata.Worktree = filepath.Join(target, "outside")
	if err := (FileMetadataStore{}).Create(metadata); err == nil {
		t.Fatal("accepted metadata for a different worktree")
	}
	if _, err := (FileMetadataStore{}).Read(target, "../bound-run"); err == nil {
		t.Fatal("accepted traversal run id")
	}
}

func TestCreateRecoversWorktreeCreatedBeforeMetadata(t *testing.T) {
	target := gitRepository(t)
	base := command(t, target, "rev-parse", "HEAD")
	worktree := filepath.Join(target, ".ai-team", "worktrees", "run-partial")
	if err := os.MkdirAll(filepath.Dir(worktree), 0755); err != nil {
		t.Fatal(err)
	}
	command(t, target, "worktree", "add", "--detach", worktree, base)
	manager, available, err := Create(context.Background(), target, "run-partial")
	if err != nil || !available || manager == nil {
		t.Fatalf("recover partial worktree: available=%v manager=%v err=%v", available, manager, err)
	}
	loaded, err := Load(context.Background(), target, "run-partial")
	canonicalWorktree, canonicalErr := filepath.EvalSymlinks(worktree)
	if err != nil || canonicalErr != nil || loaded.Root() != canonicalWorktree {
		t.Fatalf("recovered candidate metadata: manager=%v err=%v", loaded, err)
	}
}

func TestCreateRecoversWorktreeCreatedBeforeMetadataThroughInjectedStore(t *testing.T) {
	target := gitRepository(t)
	base := command(t, target, "rev-parse", "HEAD")
	worktree := filepath.Join(target, ".ai-team", "worktrees", "api-recovered")
	if err := os.MkdirAll(filepath.Dir(worktree), 0755); err != nil {
		t.Fatal(err)
	}
	command(t, target, "worktree", "add", "--detach", worktree, base)
	store := &recordingMetadataStore{}
	manager, available, err := CreateWithMetadataStore(context.Background(), target, "api-recovered", store)
	if err != nil || !available || manager == nil || store.reads != 1 || store.creates != 1 {
		t.Fatalf("recover through injected store: available=%v manager=%v store=%+v err=%v", available, manager, store, err)
	}
	loaded, err := LoadWithMetadataStore(context.Background(), target, "api-recovered", store)
	if err != nil || loaded.Root() != manager.Root() {
		t.Fatalf("load recovered candidate: manager=%v err=%v", loaded, err)
	}
}

// A live checkout with someone else's unfinished edits must not block a run:
// the candidate is cut from the base branch, not from the live working tree.
func TestCreateIgnoresDirtyLiveWorkspaceAndStartsFromBaseBranch(t *testing.T) {
	target := gitRepository(t)
	baseHead := command(t, target, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	command(t, target, "checkout", "-q", "-b", "feature-in-progress")
	if err := os.WriteFile(filepath.Join(target, "feature.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	command(t, target, "add", "feature.txt")
	command(t, target, "commit", "-q", "-m", "wip")
	manager, available, err := Create(context.Background(), target, "run-dirty")
	if err != nil || !available {
		t.Fatalf("dirty live checkout must not block the run: available=%t err=%v", available, err)
	}
	identity, err := manager.Identity()
	if err != nil || identity.BaseCommit != baseHead {
		t.Fatalf("candidate must start from master, not from the live branch: base=%s want=%s err=%v", identity.BaseCommit, baseHead, err)
	}
	if _, err := os.Stat(filepath.Join(manager.Root(), "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("live branch leaked into the candidate: %v", err)
	}
}

func TestControllerAbsenceMarkerIsRunTargetBoundAndFailClosed(t *testing.T) {
	target := t.TempDir()
	store := FileMetadataStore{}
	runID := "absence-marker-run"
	if err := store.MarkAbsent(target, runID); err != nil {
		t.Fatalf("mark absent: %v", err)
	}
	if err := store.ReadAbsent(target, runID); err != nil {
		t.Fatalf("read valid marker: %v", err)
	}
	if err := store.ReadAbsent(t.TempDir(), runID); err == nil {
		t.Fatal("absence marker crossed target scope")
	}
	if err := store.ReadAbsent(target, "another-run"); err == nil {
		t.Fatal("absence marker crossed run scope")
	}
	if err := store.MarkAbsent(target, runID); err != nil {
		t.Fatalf("idempotent mark: %v", err)
	}
	markerPath := filepath.Join(target, ".ai-team", "state", "candidates", runID+".absent.json")
	if err := os.WriteFile(markerPath, []byte(`{"schema_version":1,"run_id":"forged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.ReadAbsent(target, runID); err == nil {
		t.Fatal("corrupt marker was accepted")
	}
}

func TestControllerAbsenceMarkerCannotReplaceGitCandidateMetadata(t *testing.T) {
	target := gitRepository(t)
	runID := "git-candidate-no-absence"
	manager, available, err := Create(context.Background(), target, runID)
	if err != nil || !available || manager == nil {
		t.Fatalf("create Git candidate: available=%v err=%v", available, err)
	}
	store := FileMetadataStore{}
	if err := store.MarkAbsent(target, runID); err == nil {
		t.Fatal("candidate absence marker replaced an existing Git candidate")
	}
	if err := store.ReadAbsent(target, runID); err == nil {
		t.Fatal("candidate metadata and absence marker were accepted together")
	}
	markerPath := filepath.Join(target, ".ai-team", "state", "candidates", runID+".absent.json")
	marker := absenceMarker{SchemaVersion: absenceMarkerVersion, RunID: runID, ControlTarget: target, CreatedAt: time.Now().UTC()}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(target, runID); err == nil {
		t.Fatal("candidate metadata read ignored a conflicting absence marker")
	}
}

func TestDetectGitRepositoryRejectsBrokenRepositoryAsAbsence(t *testing.T) {
	target := gitRepository(t)
	hasGit, err := DetectGitRepository(context.Background(), target)
	if err != nil || !hasGit {
		t.Fatalf("Git target detection: hasGit=%v err=%v", hasGit, err)
	}
	gitPath := filepath.Join(target, ".git")
	if err := os.RemoveAll(gitPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitPath, []byte("gitdir: missing"), 0600); err != nil {
		t.Fatal(err)
	}
	// Corrupt state in an ancestor .git marker must not be converted into
	// controller proof that the run has no candidate.
	if _, err := DetectGitRepository(context.Background(), target); err == nil {
		t.Fatal("broken .git repository was treated as a non-Git target")
	}
}

func gitRepository(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	command(t, target, "init", "-b", CandidateBaseBranch)
	command(t, target, "config", "user.email", "test@example.com")
	command(t, target, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	command(t, target, "add", "README.md")
	command(t, target, "commit", "-m", "initial")
	return target
}

func command(t *testing.T, target string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", target}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(bytes.TrimSpace(output))
}

// With an origin the candidate starts from the freshly fetched origin/master,
// even when the live checkout is behind it.
func TestCreateFetchesOriginBaseBranchBeforeCutting(t *testing.T) {
	bare := t.TempDir()
	command(t, bare, "init", "--bare", "-b", CandidateBaseBranch)
	target := gitRepository(t)
	command(t, target, "remote", "add", "origin", bare)
	command(t, target, "push", "-q", "origin", CandidateBaseBranch)
	staleHead := command(t, target, "rev-parse", "HEAD")

	other := t.TempDir()
	command(t, other, "clone", "-q", bare, ".")
	command(t, other, "config", "user.email", "test@example.com")
	command(t, other, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(other, "newer.txt"), []byte("newer"), 0644); err != nil {
		t.Fatal(err)
	}
	command(t, other, "add", "newer.txt")
	command(t, other, "commit", "-q", "-m", "newer master")
	command(t, other, "push", "-q", "origin", CandidateBaseBranch)
	newHead := command(t, other, "rev-parse", "HEAD")

	manager, available, err := Create(context.Background(), target, "run-fetch")
	if err != nil || !available {
		t.Fatalf("create: available=%t err=%v", available, err)
	}
	identity, err := manager.Identity()
	if err != nil || identity.BaseCommit != newHead || identity.BaseCommit == staleHead {
		t.Fatalf("candidate must start from the fetched origin/master: base=%s want=%s stale=%s err=%v", identity.BaseCommit, newHead, staleHead, err)
	}
}
