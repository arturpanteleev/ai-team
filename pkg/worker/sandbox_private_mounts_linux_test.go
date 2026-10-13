//go:build linux

package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeBubblewrap puts a no-op bwrap first on PATH. The tests below only
// exercise command construction, which must fail closed before any launch.
func installFakeBubblewrap(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "bwrap"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
}

func buildBubblewrapCommandForTest(t *testing.T, target string) error {
	t.Helper()
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil,
		[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	return err
}

// Every controller-owned directory hidden behind a tmpfs must refuse a hard
// link inside it: the alias would keep the masked bytes reachable elsewhere.
func TestBubblewrapRejectsHardLinkedFileInEachControllerPrivateMount(t *testing.T) {
	for _, name := range []string{
		"candidates", "evidence", "attempt-manifests", "events", "handoff-inputs",
		"usage", "delivery", "delivery-receipts", "attestation", "containment", "briefs",
	} {
		t.Run(name, func(t *testing.T) {
			installFakeBubblewrap(t)
			target := makeBubblewrapTarget(t)
			dir := filepath.Join(target, ".ai-team", "state", name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(dir, "record.json")
			if err := os.WriteFile(original, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(original, filepath.Join(dir, "alias.json")); err != nil {
				t.Fatal(err)
			}
			err := buildBubblewrapCommandForTest(t, target)
			if err == nil || !strings.Contains(err.Error(), "hard links") || !strings.Contains(err.Error(), dir) {
				t.Fatalf("hard-linked file in private mount %s must fail closed, got %v", name, err)
			}
		})
	}
}

// A regular file squatting on a controller authority directory must stop the
// launch instead of being bound or silently skipped.
func TestBubblewrapRejectsFileInPlaceOfControllerAuthorityDirectory(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{name: "handoff-inputs", want: "prepare controller clarification input authority mount"},
		{name: "delivery-receipts", want: "prepare controller delivery receipt mount"},
		{name: "briefs", want: "prepare controller business brief mount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeBubblewrap(t)
			target := makeBubblewrapTarget(t)
			if err := os.WriteFile(filepath.Join(target, ".ai-team", "state", tc.name), []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			err := buildBubblewrapCommandForTest(t, target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("file in place of %s must fail closed with %q, got %v", tc.name, tc.want, err)
			}
		})
	}
}

// The per-run candidate evidence directory is probed for exact files to mask.
// If the run entry is not a directory, the probe error is not "absent" and the
// launch must fail instead of treating the evidence as missing.
func TestBubblewrapRejectsUninspectableCandidateEvidenceRunEntry(t *testing.T) {
	installFakeBubblewrap(t)
	target := makeBubblewrapTarget(t)
	evidenceDir := filepath.Join(target, ".ai-team", "state", "evidence")
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "sandbox-test"), []byte("not a run directory"), 0600); err != nil {
		t.Fatal(err)
	}
	err := buildBubblewrapCommandForTest(t, target)
	if err == nil || !strings.Contains(err.Error(), "inspect controller candidate evidence path") {
		t.Fatalf("non-directory candidate evidence run entry must fail closed, got %v", err)
	}
}
