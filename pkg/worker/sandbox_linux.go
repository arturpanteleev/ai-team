//go:build linux

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func checkBubblewrapAvailable() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("Linux worker isolation requires bubblewrap (bwrap): %w", err)
	}
	return nil
}

func bubblewrapWorkerCommand(ctx context.Context, worker *exec.Cmd, target, dbPath, runID string, agentPaths, environment []string) (*exec.Cmd, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return nil, fmt.Errorf("bubblewrap run id: %w", err)
	}
	canonicalTarget, err := resolveBubblewrapTarget(target)
	if err != nil {
		return nil, err
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap unavailable: %w", err)
	}
	env := envMap(environment)
	home, temp := env["HOME"], env["TMPDIR"]
	if home == "" || temp == "" || !filepath.IsAbs(home) || !filepath.IsAbs(temp) {
		return nil, errors.New("worker HOME and TMPDIR must be absolute for bubblewrap")
	}
	for _, capabilitySocket := range []struct{ key, role string }{
		{workerAPISocketEnv, "worker API capability socket"},
		{openAIEgressSocketEnv, "OpenAI egress capability socket"},
	} {
		if socketPath := env[capabilitySocket.key]; socketPath != "" {
			if err := validateWorkerSocketPath(socketPath, capabilitySocket.role, canonicalTarget, home, temp); err != nil {
				return nil, err
			}
		}
	}
	for _, bind := range []struct{ role, path string }{{"worker HOME", home}, {"worker TMPDIR", temp}} {
		if err := rejectRunBindPath(bind.path, bind.role); err != nil {
			return nil, err
		}
	}
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-net",
		// A nested runtime must not regain permission to inspect the worker's
		// initial capability environment after the worker marks itself
		// non-dumpable.
		"--cap-drop", "CAP_SYS_PTRACE",
		"--ro-bind", "/", "/",
		// Hide standard host service sockets (for example docker.sock and
		// system D-Bus sockets) while keeping a writable namespace-local /run.
		"--tmpfs", "/run",
		"--dev", "/dev", "--proc", "/proc",
		"--bind", canonicalTarget, canonicalTarget,
		"--bind", home, home,
		"--bind", temp, temp,
	}
	if env[openAIEgressSocketEnv] != "" || env[openAIEgressTokenEnv] != "" {
		if env[openAIEgressSocketEnv] == "" || env[openAIEgressTokenEnv] == "" {
			return nil, errors.New("OpenAI egress socket and capability must be configured together")
		}
		// The worker-side HTTP proxy binds namespace-local loopback. Giving it
		// NET_ADMIN inside this private network namespace lets it bring `lo` up;
		// this does not grant access to the host network namespace.
		args = append(args, "--cap-add", "CAP_NET_ADMIN")
	}
	for _, path := range agentPaths {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return nil, fmt.Errorf("agent registry path %q: %w", path, resolveErr)
		}
		if err := rejectRunBindPath(resolved, "agent registry path"); err != nil {
			return nil, err
		}
		args = append(args, "--ro-bind", resolved, resolved)
	}
	// Lifecycle checkpoints and legacy file approvals are controller-owned.
	// Their directories are overlaid with private tmpfs mounts after the target
	// bind, so neither target access nor the controller's host paths expose them.
	lifecycleDir := filepath.Join(canonicalTarget, ".ai-team", "state", "runs")
	if err := appendPrivateDirectoryMount(&args, lifecycleDir, true); err != nil {
		return nil, err
	}
	legacyApprovalsDir := filepath.Join(canonicalTarget, ".ai-team", "state", "approvals")
	if err := appendPrivateDirectoryMount(&args, legacyApprovalsDir, false); err != nil {
		return nil, err
	}
	// Candidate identity is persisted by the controller API. Keep candidate
	// worktree files writable, while masking this controller metadata directory.
	candidateMetadataDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "candidates")
	if err != nil {
		return nil, fmt.Errorf("prepare candidate metadata mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, candidateMetadataDir, true); err != nil {
		return nil, err
	}
	candidateEvidenceDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "evidence")
	if err != nil {
		return nil, fmt.Errorf("prepare candidate evidence mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, candidateEvidenceDir, true); err != nil {
		return nil, err
	}
	// Canonical attempt manifests are controller-owned. Keep the worker-visible
	// attempts/<id>/inputs and artifacts snapshots under .ai-team/runs intact,
	// but hide only the separate authority store from direct child access.
	attemptManifestDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "attempt-manifests")
	if err != nil {
		return nil, fmt.Errorf("prepare attempt manifest authority mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, attemptManifestDir, true); err != nil {
		return nil, err
	}
	// The namespace-local shadow is read-only too: otherwise a child could
	// create a counterfeit file at the controller authority path and confuse
	// tools running in the same sandbox, even though host storage stayed safe.
	args = append(args, "--chmod", "0555", attemptManifestDir)
	eventAuthorityDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "events")
	if err != nil {
		return nil, fmt.Errorf("prepare controller event authority mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, eventAuthorityDir, true); err != nil {
		return nil, err
	}
	args = append(args, "--chmod", "0555", eventAuthorityDir)
	candidateEvidenceRunDir := filepath.Join(candidateEvidenceDir, runID)
	candidateEvidenceRunDirAdded := false
	for _, name := range []string{"review-candidate.json", "verification-candidate.json"} {
		candidateEvidencePath := filepath.Join(candidateEvidenceDir, runID, name)
		if info, statErr := os.Lstat(candidateEvidencePath); statErr == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("controller candidate evidence path %q must be a regular file", candidateEvidencePath)
			}
			if !candidateEvidenceRunDirAdded {
				args = append(args, "--dir", candidateEvidenceRunDir)
				candidateEvidenceRunDirAdded = true
			}
			args = append(args, "--ro-bind", "/dev/null", candidateEvidencePath)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect controller candidate evidence path: %w", statErr)
		}
	}
	usageDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "usage")
	if err != nil {
		return nil, fmt.Errorf("prepare usage envelope mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, usageDir, true); err != nil {
		return nil, err
	}
	deliveryDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "delivery")
	if err != nil {
		return nil, fmt.Errorf("prepare terminal delivery mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, deliveryDir, true); err != nil {
		return nil, err
	}
	// Delivery receipts prove that trusted controller code observed a successful
	// Execute. The worker API can submit terminal-record mirrors, but this
	// separate authority store has no worker writer and is hidden from children.
	deliveryReceiptDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "delivery-receipts")
	if err != nil {
		return nil, fmt.Errorf("prepare controller delivery receipt mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, deliveryReceiptDir, true); err != nil {
		return nil, err
	}
	args = append(args, "--chmod", "0555", deliveryReceiptDir, "--remount-ro", deliveryReceiptDir)
	attestationDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "attestation")
	if err != nil {
		return nil, fmt.Errorf("prepare controller attestation mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, attestationDir, true); err != nil {
		return nil, err
	}
	containmentDir, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "containment")
	if err != nil {
		return nil, fmt.Errorf("prepare controller containment mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, containmentDir, true); err != nil {
		return nil, err
	}
	containmentRecordPath := filepath.Join(containmentDir, runID+".json")
	if info, statErr := os.Lstat(containmentRecordPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("controller containment path %q must be a regular file", containmentRecordPath)
		}
		args = append(args, "--ro-bind", "/dev/null", containmentRecordPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect controller containment path: %w", statErr)
	}
	attestationRecordPath := filepath.Join(attestationDir, runID+".json")
	if info, statErr := os.Lstat(attestationRecordPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("controller attestation path %q must be a regular file", attestationRecordPath)
		}
		args = append(args, "--ro-bind", "/dev/null", attestationRecordPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect controller attestation path: %w", statErr)
	}
	deliveryRecordPath := filepath.Join(deliveryDir, runID+".json")
	if info, statErr := os.Lstat(deliveryRecordPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("controller delivery record path %q must be a regular file", deliveryRecordPath)
		}
		args = append(args, "--ro-bind", "/dev/null", deliveryRecordPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect controller delivery record path: %w", statErr)
	}
	if err := evidence.ValidateRunID(runID); err != nil {
		return nil, fmt.Errorf("business brief run id: %w", err)
	}
	briefRoot, err := safeio.EnsureDir(canonicalTarget, ".ai-team", "state", "briefs")
	if err != nil {
		return nil, fmt.Errorf("prepare controller business brief mount: %w", err)
	}
	if err := appendPrivateDirectoryMount(&args, briefRoot, true); err != nil {
		return nil, err
	}
	// Keep the namespace-local shadow read-only. The complete root is hidden so
	// a worker cannot inspect or alter briefs belonging to another run. Brief
	// versions are accessed through the controller API; workers receive
	// materialized copies in their workspace for path-based artifact publication.
	args = append(args, "--chmod", "0555", briefRoot)
	resolvedDB, err := resolveExistingPath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("controller database path: %w", err)
	}
	// SQLite may use WAL, SHM, or rollback journal sidecars. Mounting /dev/null
	// over each exact path prevents file contents from reaching the worker;
	// workers receive controller operations through the scoped API instead.
	protectedPaths := make([]string, 0, 4)
	for _, path := range []string{resolvedDB, resolvedDB + "-wal", resolvedDB + "-shm", resolvedDB + "-journal"} {
		canonical, checkErr := resolveAndCheckDatabasePath(path)
		if checkErr != nil {
			return nil, checkErr
		}
		protectedPaths = append(protectedPaths, canonical)
	}
	for _, path := range protectedPaths {
		args = append(args, "--ro-bind", "/dev/null", path)
	}
	args = append(args, "--chdir", canonicalTarget, "--")
	args = append(args, worker.Path)
	args = append(args, worker.Args[1:]...)
	return exec.CommandContext(ctx, bwrap, args...), nil
}

// resolveBubblewrapTarget canonicalizes the writable workspace bind. Binding
// the host filesystem root read-write would undo the preceding read-only root
// mount, so reject every spelling/symlink that resolves to it.
func resolveBubblewrapTarget(target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve worker target %q: %w", target, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve worker target %q: %w", target, err)
	}
	resolved = filepath.Clean(resolved)
	if resolved == string(filepath.Separator) {
		return "", errors.New("worker target must not resolve to the filesystem root")
	}
	if err := rejectRunBindPath(resolved, "worker target"); err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect worker target %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("worker target %q must be a directory", resolved)
	}
	return resolved, nil
}

// rejectRunBindPath prevents a later bind from covering the private /run
// tmpfs with host content. Resolve both sides so symlink aliases cannot bypass
// the check.
func rejectRunBindPath(path, role string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s bind path %q: %w", role, path, err)
	}
	runRoot, err := filepath.EvalSymlinks("/run")
	if err != nil {
		return fmt.Errorf("resolve private /run mount root: %w", err)
	}
	relative, err := filepath.Rel(filepath.Clean(runRoot), filepath.Clean(canonical))
	if err != nil {
		return fmt.Errorf("compare %s bind path with /run: %w", role, err)
	}
	insideRun := relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	runRelative, err := filepath.Rel(filepath.Clean(canonical), filepath.Clean(runRoot))
	if err != nil {
		return fmt.Errorf("compare /run with %s bind path: %w", role, err)
	}
	pathContainsRun := runRelative == "." || (runRelative != ".." && !strings.HasPrefix(runRelative, ".."+string(filepath.Separator)))
	if insideRun || pathContainsRun {
		return fmt.Errorf("%s bind path %q overlaps /run; refusing to reopen the private /run mount", role, canonical)
	}
	return nil
}

// resolveAndCheckDatabasePath canonicalizes an existing DB/sidecar before it
// is masked. A second hard link would leave the same bytes reachable at an
// unmasked pathname, so refuse to launch when an existing file has aliases.
func resolveAndCheckDatabasePath(path string) (string, error) {
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve controller database path %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return resolved, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect controller database path %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("controller database path %q must be a regular file", resolved)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot verify hard-link count for controller database path %q", resolved)
	}
	if stat.Nlink > 1 {
		return "", fmt.Errorf("controller database path %q has %d hard links; refusing bubblewrap launch because an alias could bypass masking", resolved, stat.Nlink)
	}
	return resolved, nil
}

func appendPrivateDirectoryMount(args *[]string, path string, required bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect private worker path %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private worker path %q must be a real directory", path)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		return err
	}
	*args = append(*args, "--tmpfs", path)
	return nil
}

// verifyPrivateDirectory rejects aliases that would remain readable elsewhere
// after the directory itself is hidden by a tmpfs mount.
func verifyPrivateDirectory(path string) error {
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect private worker path %q: %w", current, err)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private worker path %q must contain only regular files and directories", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot verify hard-link count for private worker path %q", current)
		}
		if stat.Nlink > 1 {
			return fmt.Errorf("private worker path %q has %d hard links; refusing bubblewrap launch because an alias could bypass masking", current, stat.Nlink)
		}
		return nil
	})
}

func envMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func resolveExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("resolve database parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
