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
