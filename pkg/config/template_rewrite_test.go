package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rewriteActiveYAML = "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"

const rewriteOldYAML = `schema_version: 6
template: t
title: Old
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
checks:
  - {name: evil, class: unit, command: [sh, -c, id], policy: required, timeout: 5m}
`

// rewriteTarget builds the one-off migration scenario: an old web publication
// with a foreign check is kept as an immutable version and pinned to a run,
// while the active file configuration is a clean one.
func rewriteTarget(t *testing.T) (string, *TemplateStore, string) {
	t.Helper()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(rewriteActiveYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, activeVersion, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish([]byte(rewriteOldYAML), activeVersion); err != nil {
		t.Fatal(err)
	}
	oldVersion := TemplateVersionID([]byte(rewriteOldYAML))
	if _, err := store.Publish([]byte(rewriteActiveYAML), oldVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinDataForRun("run-old", "run-old", []byte(rewriteOldYAML)); err != nil {
		t.Fatal(err)
	}
	return target, store, oldVersion
}

func markerPath(target, id string) string {
	return filepath.Join(target, ".ai-team", "templates", trustedVersionsDirName, id)
}

// The old version is found by the audit and refused by every run path until an
// operator explicitly re-saves it. The dry-run part of the audit is read-only:
// no anchor and no change to .ai-team/config.yaml.
func TestRewriteAnchorsOldVersionWithoutTouchingFileConfig(t *testing.T) {
	target, store, oldVersion := rewriteTarget(t)

	if _, _, _, err := store.ReadRunnablePinnedRun("run-old"); !errors.As(err, new(*TrustedConfigError)) {
		t.Fatalf("an old version without an anchor must be refused at launch: %v", err)
	}
	findings, err := store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		if finding.Version == oldVersion && strings.Contains(strings.Join(finding.Problems, " "), "checks.evil") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the audit must name the old version and the reason: %+v", findings)
	}
	if _, err := os.Stat(markerPath(target, oldVersion)); !os.IsNotExist(err) {
		t.Fatalf("a dry-run must not write the trust anchor: %v", err)
	}
	active, err := os.ReadFile(filepath.Join(target, ".ai-team", "config.yaml"))
	if err != nil || string(active) != rewriteActiveYAML {
		t.Fatalf("the audit must not change the file config: %q, %v", active, err)
	}

	if err := store.RewriteVersion(oldVersion); err != nil {
		t.Fatalf("explicit re-save: %v", err)
	}
	if _, err := os.Stat(markerPath(target, oldVersion)); err != nil {
		t.Fatalf("the re-save must write the trusted file anchor: %v", err)
	}
	active, err = os.ReadFile(filepath.Join(target, ".ai-team", "config.yaml"))
	if err != nil || string(active) != rewriteActiveYAML {
		t.Fatalf("the re-save must not change the file config: %q, %v", active, err)
	}

	// Anchored now, the same pinned version runs.
	if _, _, found, err := store.ReadRunnablePinnedRun("run-old"); !found || err != nil {
		t.Fatalf("the re-saved version must run: found=%v err=%v", found, err)
	}
	// It is no longer reported as a finding either.
	findings, err = store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Version == oldVersion {
			t.Fatalf("a re-saved version must not be audited again: %+v", finding)
		}
	}
}

// A re-save grants trust only to the named bytes. Another unreviewed version
// with a different check stays refused, and a version matching the human file
// keeps running.
func TestRewriteDoesNotWeakenTrustedConfigOnly(t *testing.T) {
	_, store, oldVersion := rewriteTarget(t)
	if err := store.RewriteVersion(oldVersion); err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(rewriteOldYAML, "name: evil", "name: other", 1)
	if _, err := store.PinDataForRun("run-other", "run-other", []byte(other)); err != nil {
		t.Fatal(err)
	}
	var refused *TrustedConfigError
	if _, _, _, err := store.ReadRunnablePinnedRun("run-other"); !errors.As(err, &refused) || refused.Code() != ErrCodeTrustedConfigOnly {
		t.Fatalf("an unreviewed version must still be refused: %v", err)
	}
	if err := store.RequireTrusted([]byte(rewriteActiveYAML), "human file"); err != nil {
		t.Fatalf("a trusted file version must keep running: %v", err)
	}
}

// A web publication made before the registry existed is still an active
// config.yaml that trusts itself. The audit lists it as a candidate, and an
// explicit re-save anchors those exact bytes without editing the file.
func TestAuditFindsPreRegistryActiveWebWrite(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(rewriteOldYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	activeVersion := TemplateVersionID([]byte(rewriteOldYAML))
	if err := store.writeImmutableVersion(activeVersion, []byte(rewriteOldYAML)); err != nil {
		t.Fatal(err)
	}
	findings, err := store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		if finding.Version == activeVersion && strings.Contains(strings.Join(finding.Problems, " "), "якоря доверия") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a pre-registry active web write must be a candidate: %+v", findings)
	}
	if err := store.RewriteVersion(activeVersion); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(filepath.Join(target, ".ai-team", "config.yaml"))
	if err != nil || string(active) != rewriteOldYAML {
		t.Fatalf("the re-save must not change the file config: %q, %v", active, err)
	}
	findings, err = store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Version == activeVersion {
			t.Fatalf("the re-saved active version must not be a finding: %+v", finding)
		}
	}
}

// The rewrite is explicit and digest-verified: bad ids and tampered versions
// never produce an anchor.
func TestRewriteRefusesUnknownOrTamperedVersion(t *testing.T) {
	target, store, oldVersion := rewriteTarget(t)
	if err := store.RewriteVersion("not-a-sha"); err == nil {
		t.Error("a malformed version id must be refused")
	}
	versionPath := filepath.Join(target, ".ai-team", "templates", "versions", oldVersion+".yaml")
	if err := os.Remove(versionPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionPath, []byte(rewriteOldYAML+"# tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.RewriteVersion(oldVersion); err == nil {
		t.Error("a tampered version must not be anchored")
	}
	if _, err := os.Stat(markerPath(target, oldVersion)); !os.IsNotExist(err) {
		t.Fatalf("a refused rewrite must not write an anchor: %v", err)
	}
}
