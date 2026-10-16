package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RunnableActiveYAML is the file configuration of the tests below.
const RunnableActiveYAML = "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"

// OldCommandTemplate is what a template.edit holder could publish through the
// web before the trusted-config rule: a host-command gate.
const OldCommandTemplate = `schema_version: 6
template: t
title: Old
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
    exit_gate_groups:
      - - gate: external_program
          version: 1
          name: evil
          params: {command: ["sh", "-c", "id"], timeout_seconds: 30}
`

func runnableStore(t *testing.T, active string) *TemplateStore {
	t.Helper()
	target := t.TempDir()
	if active != "" {
		if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(active), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestReadRunnablePinnedRunRefusesCommandGateTemplate(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML)
	if _, err := store.PinDataForRun("run-old", "run-old", []byte(OldCommandTemplate)); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := store.ReadRunnablePinnedRun("run-old")
	var refused *TrustedConfigError
	if !errors.As(err, &refused) || refused.Code() != ErrCodeTrustedConfigOnly {
		t.Fatalf("a pin with a command gate must be refused with trusted_config_only, got %v", err)
	}
	if !strings.Contains(err.Error(), "external_program") || !strings.Contains(err.Error(), ErrCodeTrustedConfigOnly) {
		t.Fatalf("the refusal must say what and why: %v", err)
	}
	// The plain reader (display) still works.
	if _, _, found, readErr := store.ReadPinnedRun("run-old"); readErr != nil || !found {
		t.Fatalf("ReadPinnedRun must stay readable: %v %v", found, readErr)
	}
}

func TestReadRunnablePinnedRunAllowsActiveProcessChangesAndMissingFileConfig(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML)
	if _, err := store.PinDataForRun("run-active", "run-active", []byte(RunnableActiveYAML)); err != nil {
		t.Fatal(err)
	}
	processOnly := strings.Replace(RunnableActiveYAML, "title: T", "title: Renamed", 1)
	if _, err := store.PinDataForRun("run-process", "run-process", []byte(processOnly)); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{"run-active", "run-process", "run-unpinned"} {
		if _, _, _, err := store.ReadRunnablePinnedRun(run); err != nil {
			t.Errorf("%s must be runnable: %v", run, err)
		}
	}
	noFile := runnableStore(t, "")
	if err := noFile.RequireTrusted([]byte(OldCommandTemplate), "x"); err != nil {
		t.Errorf("without a file configuration there is nothing to compare with: %v", err)
	}
}

func TestRequireTrustedRefusesInfrastructureDrift(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML)
	drift := RunnableActiveYAML + "containment: {profile: trusted-local}\n"
	err := store.RequireTrusted([]byte(drift), "pin")
	var refused *TrustedConfigError
	if !errors.As(err, &refused) {
		t.Fatalf("infrastructure drift must be refused: %v", err)
	}
}

// A paused task keeps running when the file only changed process (roles,
// routes, limits) or tuning (timeouts, model, effort) since the run was pinned.
func TestRequireTrustedToleratesProcessAndTuningDrift(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML+"stage_timeout: 30m\nmodel: m1\n")
	pinned := strings.Replace(RunnableActiveYAML, "function: po", "function: developer", 1) +
		"stage_timeout: 1h\nmodel: m2\neffort: high\nforbid_self_approval: true\n"
	if err := store.RequireTrusted([]byte(pinned), "pin"); err != nil {
		t.Fatalf("process and tuning drift must not block a run: %v", err)
	}
	// Tuning is still infrastructure for a web submission.
	web, err := ParseYAML([]byte(pinned))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseYAML([]byte(RunnableActiveYAML + "stage_timeout: 30m\nmodel: m1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if problems := web.UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Fatal("the web must still not change timeouts or models")
	}
}
