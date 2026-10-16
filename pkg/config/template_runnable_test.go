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
}

// Without a file configuration nothing is trusted: a pin with commands or
// infrastructure is refused, a plain one keeps running.
func TestRequireTrustedWithoutFileConfigRefusesCommandsOnly(t *testing.T) {
	noFile := runnableStore(t, "")
	var refused *TrustedConfigError
	if err := noFile.RequireTrusted([]byte(OldCommandTemplate), "x"); !errors.As(err, &refused) {
		t.Errorf("a command pin without a file configuration must be refused: %v", err)
	}
	if err := noFile.RequireTrusted([]byte(RunnableActiveYAML+"containment: {profile: trusted-local}\n"), "x"); !errors.As(err, &refused) {
		t.Errorf("an infrastructure pin without a file configuration must be refused: %v", err)
	}
	if err := noFile.RequireTrusted([]byte(RunnableActiveYAML), "x"); err != nil {
		t.Errorf("a pin without commands must keep running when the file never existed: %v", err)
	}
	if _, _, found, err := noFile.ReadRunnablePinnedRun("nothing"); err != nil || found {
		t.Errorf("an unpinned run is not an error: %v %v", found, err)
	}
}

// The active template is not trusted by being active: an old web version that
// stayed active is refused until a person edits the file.
func TestActiveWebWrittenCommandTemplateIsRefusedUntilEdited(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML)
	_, version, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish([]byte(OldCommandTemplate), version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinCurrentForRun("run-web", "run-web"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ReadRunnablePinnedRun("run-web")
	var refused *TrustedConfigError
	if !errors.As(err, &refused) || !refused.WebWritten {
		t.Fatalf("the active web-written command template must be refused: %v", err)
	}
	if !strings.Contains(err.Error(), "записан через веб") {
		t.Fatalf("the refusal must name the way out: %v", err)
	}
	// A process-only web publication on top of the human file still runs.
	process := strings.Replace(RunnableActiveYAML, "title: T", "title: Renamed", 1)
	if err := store.RequireTrusted([]byte(process), "pin"); err != nil {
		t.Fatalf("process-only template must run against the human baseline: %v", err)
	}

	// A person reviews and edits the file: its hash changes, it is trusted again.
	edited := []byte(OldCommandTemplate + "# reviewed\n")
	if err := os.WriteFile(filepath.Join(store.target, ".ai-team", "config.yaml"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinCurrentForRun("run-edited", "run-edited"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ReadRunnablePinnedRun("run-edited"); err != nil {
		t.Fatalf("a hand-edited file is trusted code: %v", err)
	}
	// The earlier pin carries the same gate in the same stage as the reviewed
	// file, so it runs too.
	if err := store.RequireTrusted([]byte(OldCommandTemplate), "old pin"); err != nil {
		t.Fatalf("a gate the reviewed file has verbatim is trusted: %v", err)
	}
}

func TestAuditIncludesActiveWebWrittenVersion(t *testing.T) {
	store := runnableStore(t, RunnableActiveYAML)
	_, version, _ := store.ReadCurrent()
	if _, err := store.Publish([]byte(OldCommandTemplate), version); err != nil {
		t.Fatal(err)
	}
	findings, err := store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	active := TemplateVersionID([]byte(OldCommandTemplate))
	if len(findings) != 1 || findings[0].Version != active || !strings.Contains(strings.Join(findings[0].Problems, "\n"), "external_program") {
		t.Fatalf("the active web-written command template must be a finding: %+v", findings)
	}
	// After a manual edit the file is the baseline and is not flagged.
	if err := os.WriteFile(filepath.Join(store.target, ".ai-team", "config.yaml"), []byte(OldCommandTemplate+"# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err = store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Version == TemplateVersionID([]byte(OldCommandTemplate+"# ok\n")) {
			t.Fatalf("a hand-edited active file must not be flagged: %+v", finding)
		}
	}
}

const runnableGateFileYAML = `schema_version: 6
template: t
title: T
cli: opencode
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
    exit_gate_groups:
      - - gate: external_program
          version: 1
          name: lint
          params: {command: ["make", "lint"], timeout_seconds: 30}
`

// A command gate of a pin is allowed when the file has the same gate in the
// same stage, so editing the file elsewhere does not strand the run; a gate
// that differs, or infrastructure that moved, is refused with a way out.
func TestRequireTrustedComparesPinnedCommandGateWithFile(t *testing.T) {
	store := runnableStore(t, runnableGateFileYAML)
	renamed := strings.Replace(runnableGateFileYAML, "title: T", "title: Renamed", 1)
	if err := store.RequireTrusted([]byte(renamed), "pin"); err != nil {
		t.Fatalf("a pin whose command gate matches the file must run: %v", err)
	}
	other := strings.Replace(runnableGateFileYAML, `"make", "lint"`, `"make", "evil"`, 1)
	err := store.RequireTrusted([]byte(other), "pin")
	var refused *TrustedConfigError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "external_program") {
		t.Fatalf("a different command must be refused: %v", err)
	}
	moved := strings.Replace(runnableGateFileYAML, "cli: opencode", "cli: claude", 1)
	err = store.RequireTrusted([]byte(moved), "pin")
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "cli") || !strings.Contains(err.Error(), "новую задачу") {
		t.Fatalf("a moved infrastructure setting must be refused with a way out: %v", err)
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
