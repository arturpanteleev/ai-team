package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestWarnAboutStoredTemplateCommands(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	active := "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(active), 0o644); err != nil {
		t.Fatal(err)
	}
	var quiet bytes.Buffer
	if err := warnAboutStoredTemplateCommands(&quiet, target); err != nil {
		t.Fatal(err)
	}
	if quiet.Len() != 0 {
		t.Fatalf("a clean store must not warn: %s", quiet.String())
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, version, _ := store.ReadCurrent()
	old := active + "checks:\n  - {name: evil, class: unit, command: [sh, -c, id], policy: required, timeout: 5m}\n"
	if _, err := store.Publish([]byte(old), version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish([]byte(active), config.TemplateVersionID([]byte(old))); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := warnAboutStoredTemplateCommands(&out, target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "trusted_config_only") {
		t.Fatalf("the warning must say that such versions are refused: %s", out.String())
	}
	if !strings.Contains(out.String(), "checks.evil") || !strings.Contains(out.String(), config.TemplateVersionID([]byte(old))) {
		t.Fatalf("the warning must name the stored version and the foreign check: %s", out.String())
	}
}

// The audit reports why it could not run instead of staying silent.
func TestWarnAboutStoredTemplateCommandsReturnsErrors(t *testing.T) {
	var out bytes.Buffer
	if err := warnAboutStoredTemplateCommands(&out, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("an unusable target must be reported")
	}
	// No project configuration: nothing to compare with, not an error.
	if err := warnAboutStoredTemplateCommands(&out, t.TempDir()); err != nil {
		t.Errorf("a target without .ai-team is not an audit failure: %v", err)
	}
	// A broken active template is an audit failure.
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte("stages: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := warnAboutStoredTemplateCommands(&out, target); err == nil {
		t.Error("an unparseable active template must be reported")
	}
	if out.Len() != 0 {
		t.Errorf("errors are returned, not printed as findings: %s", out.String())
	}
}
