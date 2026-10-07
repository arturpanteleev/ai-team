package candidate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

func TestCreateRejectsDirtyLiveWorkspace(t *testing.T) {
	target := gitRepository(t)
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, available, err := Create(context.Background(), target, "run-dirty"); err == nil || !available {
		t.Fatalf("dirty workspace принят: available=%t err=%v", available, err)
	}
}

func gitRepository(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	command(t, target, "init", "-b", "main")
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
