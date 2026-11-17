//go:build !windows

package gaterun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func writeGateTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// gitOverlays walks the repository to find nested git metadata. On a symlinked
// root WalkDir does not descend, so the daemon would get an unchecked tree.
func TestGitOverlaysRejectsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	writeGateTree(t, target, map[string]string{"sub/keep": "x"})
	if err := os.Symlink(t.TempDir(), filepath.Join(target, "sub", ".git")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOverlays(link); err == nil {
		t.Fatal("a symlinked repository root was accepted")
	}
}

func TestGitOverlaysWalksThroughAncestorSymlink(t *testing.T) {
	real := t.TempDir()
	writeGateTree(t, real, map[string]string{"sub/keep": "x"})
	if err := os.Symlink(t.TempDir(), filepath.Join(real, "sub", ".git")); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Symlink(real, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOverlays(filepath.Join(parent, "link")); err == nil {
		t.Fatal("nested .git behind an ancestor symlink was not checked")
	}
}

func TestGitOverlaysRejectsGitShortNames(t *testing.T) {
	root := t.TempDir()
	writeGateTree(t, root, map[string]string{"sub/keep": "x", "other/GIT~2": "garbage"})
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "sub", "GIT~1")); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOverlays(root); err == nil {
		t.Fatal("GIT~1-style aliases were accepted")
	}
}

func TestHasGitElementRecognisesShortNames(t *testing.T) {
	for path, want := range map[string]bool{"GIT~1/config": true, "a/.GIT~2": true, "a/github~1": false, "a/src": false} {
		if got := hasGitElement(path); got != want {
			t.Errorf("hasGitElement(%q) = %v, want %v", path, got, want)
		}
	}
}

func dockerHardenSpec(t *testing.T, repoRoot string, writable bool) LaunchSpec {
	t.Helper()
	settings := &DockerSettings{RepoWritable: writable, Image: testImage}
	return LaunchSpec{
		Argv:           []string{"/bin/sh", "/gate/repo/gate.sh"},
		MaxOutputBytes: 4096,
		Input:          Input{Paths: PathsInfo{RepoRoot: repoRoot, EvidenceDir: t.TempDir()}},
		Docker:         settings,
	}
}

// The Docker path refuses a symlinked WorkDir before talking to the daemon.
func TestDockerLauncherRejectsSymlinkedRepoRoot(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	target := t.TempDir()
	writeGateTree(t, target, map[string]string{"sub/keep": "x"})
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	spec := dockerHardenSpec(t, link, false)
	out := DockerLauncher{Docker: docker}.Launch(context.Background(), spec)
	if out.Err == nil || !strings.Contains(out.Err.Error(), "symlink") || out.ExitCode != -1 {
		t.Fatalf("symlinked repo root was launched: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err == nil {
		t.Fatal("the daemon was invoked for a symlinked repo root")
	}
}

// A legitimate repository still passes through the whole Docker adapter,
// including the nested git walk of a writable repository.
func TestDockerLauncherAcceptsLegitimateRepoRoot(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	repo := t.TempDir()
	writeGateTree(t, repo, map[string]string{".git/HEAD": "ref: x", "gate.sh": "echo ok"})
	spec := dockerHardenSpec(t, repo, true)
	out := DockerLauncher{Docker: docker}.Launch(context.Background(), spec)
	if out.Err != nil || out.ExitCode != 0 {
		t.Fatalf("legitimate repository rejected: %+v (%v)", out, out.Err)
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err != nil {
		t.Fatalf("the daemon was not invoked: %v", err)
	}
}

// Launch is exported and validates its own root: an empty or relative path
// must not silently become the controller's working directory.
func TestDockerLauncherRejectsNonAbsoluteRepoRoot(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	for _, root := range []string{"", ".", "repo/relative"} {
		out := DockerLauncher{Docker: docker}.Launch(context.Background(), dockerHardenSpec(t, root, false))
		if out.Err == nil || !strings.Contains(out.Err.Error(), "absolute path") || out.ExitCode != -1 {
			t.Fatalf("repo root %q was launched: %+v", root, out)
		}
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err == nil {
		t.Fatal("the daemon was invoked for a non-absolute repo root")
	}
}

// Every repository mount and every overlay must be based on the resolved root:
// the daemon must never be handed the spelling that still contains a symlink.
func TestDockerLauncherMountsResolvedRootBehindAncestorSymlink(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	realRepo := filepath.Join(parent, "repo")
	writeGateTree(t, realRepo, map[string]string{".git/HEAD": "ref: x", "build/keep": "x", "gate.sh": "echo ok"})
	link := filepath.Join(base, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	shown := filepath.Join(link, "repo")
	resolved, err := filepath.EvalSymlinks(shown)
	if err != nil {
		t.Fatal(err)
	}
	spec := dockerHardenSpec(t, shown, true)
	spec.Docker.Mounts = []DockerMount{{Path: "build", Target: "/build"}}
	out := DockerLauncher{Docker: docker}.Launch(context.Background(), spec)
	if out.Err != nil || out.ExitCode != 0 {
		t.Fatalf("launch failed: %+v (%v)", out, out.Err)
	}
	args := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(fakeDir, "run.args"))), "\n")
	var mounts []string
	for i, a := range args {
		if a == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	for _, want := range []string{
		"type=bind,source=" + resolved + ",target=/gate/repo",
		"type=bind,source=" + filepath.Join(resolved, ".git") + ",target=/gate/repo/.git,readonly",
		"type=bind,source=" + filepath.Join(resolved, "build") + ",target=/build,readonly",
	} {
		found := false
		for _, m := range mounts {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mounts lack %q:\n%q", want, mounts)
		}
	}
	for _, m := range mounts {
		if strings.Contains(m, "source="+shown) || strings.Contains(m, "source="+filepath.Join(shown, "build")) {
			t.Errorf("a mount still uses the unresolved root: %q", m)
		}
	}
}

// The fake git executable is the hook between the git guard and docker run:
// it swaps an ancestor symlink while gitOverlays is still inspecting the tree.
// The launcher must re-verify before the daemon sees any mount.
func TestDockerLauncherRejectsRootSwappedAfterGuard(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	realRepo := filepath.Join(parent, "repo")
	writeGateTree(t, realRepo, map[string]string{".git/HEAD": "ref: x", "gate.sh": "echo ok"})
	link := filepath.Join(base, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	git := `#!/bin/sh
rm -f "$SWAP_LINK"
ln -s "$SWAP_TARGET" "$SWAP_LINK"
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWAP_LINK", link)
	t.Setenv("SWAP_TARGET", other)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	spec := dockerHardenSpec(t, filepath.Join(link, "repo"), true)
	out := DockerLauncher{Docker: docker}.Launch(context.Background(), spec)
	if out.Err == nil || !strings.Contains(out.Err.Error(), "changed after the check") || out.ExitCode != -1 {
		t.Fatalf("a root swapped after the guard was launched: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err == nil {
		t.Fatal("the daemon was invoked after the root was swapped")
	}
}

func TestDockerRepoRootPinDetectsSwapAfterTheCheck(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGateTree(t, root, map[string]string{".git/HEAD": "ref: x"})
	pin, err := safeio.PinRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitOverlays(pin.Real()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if err := pin.Verify(); err == nil {
		t.Fatal("a root swapped after the check was accepted")
	}
}
