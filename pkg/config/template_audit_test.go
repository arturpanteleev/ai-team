package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditVersionsFlagsStoredCommandsAbsentFromTheActiveTemplate(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	active := `schema_version: 6
template: t
title: T
stages:
  - {id: spec, title: Spec, function: po, result: md, executor: human}
`
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(active), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, activeVersion, _ := store.ReadCurrent()
	old := active + "checks:\n  - {name: evil, class: unit, command: [sh, -c, id], policy: required, timeout: 5m}\n"
	if _, err := store.Publish([]byte(old), activeVersion); err != nil {
		t.Fatal(err)
	}
	oldVersion := TemplateVersionID([]byte(old))
	if _, err := store.Publish([]byte(active), oldVersion); err != nil {
		t.Fatal(err)
	}
	findings, err := store.AuditVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Version != oldVersion || !strings.Contains(strings.Join(findings[0].Problems, " "), "checks.evil") {
		t.Fatalf("the stored version with a foreign check must be reported alone: %+v", findings)
	}
}
