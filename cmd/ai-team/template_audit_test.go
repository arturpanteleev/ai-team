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
	warnAboutStoredTemplateCommands(&quiet, target)
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
	warnAboutStoredTemplateCommands(&out, target)
	if !strings.Contains(out.String(), "checks.evil") || !strings.Contains(out.String(), config.TemplateVersionID([]byte(old))) {
		t.Fatalf("the warning must name the stored version and the foreign check: %s", out.String())
	}
}
