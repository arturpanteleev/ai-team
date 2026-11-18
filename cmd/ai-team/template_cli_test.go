package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

// templateAuditTarget mirrors the one-off migration: an old web publication
// with a foreign check stays as a stored version pinned to a run; the active
// file configuration is clean.
func templateAuditTarget(t *testing.T) (string, string) {
	t.Helper()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	active := "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(active), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(active, "title: T", "title: Old", 1) +
		"checks:\n  - {name: evil, class: unit, command: [sh, -c, id], policy: required, timeout: 5m}\n"
	if _, err := store.Publish([]byte(old), version); err != nil {
		t.Fatal(err)
	}
	oldVersion := config.TemplateVersionID([]byte(old))
	if _, err := store.Publish([]byte(active), oldVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinDataForRun("run-old", "run-old", []byte(old)); err != nil {
		t.Fatal(err)
	}
	return target, oldVersion
}

func TestTemplateAuditDryRunThenExplicitRewrite(t *testing.T) {
	target, oldVersion := templateAuditTarget(t)
	activePath := filepath.Join(target, ".ai-team", "config.yaml")
	activeBefore, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, ".ai-team", "templates", "trusted-versions", oldVersion)

	// Dry-run by default: lists the version and the reason, writes nothing.
	stdout, code, stderr := runCLI(t, "template", "audit", "--target", target)
	if code != 0 {
		t.Fatalf("template audit: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, oldVersion) || !strings.Contains(stdout, "checks.evil") {
		t.Fatalf("the dry-run must name the version and the reason: %s", stdout)
	}
	if !strings.Contains(stdout, "dry-run") || !strings.Contains(stdout, "ничего не изменено") {
		t.Fatalf("the dry-run must say it changed nothing: %s", stdout)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write the anchor: %v", err)
	}

	// The pinned version is still refused at launch after a dry-run.
	if _, _, err := loadPinnedResumeConfig(target, "run-old", nil); err == nil ||
		!strings.Contains(err.Error(), config.ErrCodeTrustedConfigOnly) {
		t.Fatalf("a dry-run must not make the old version runnable: %v", err)
	}

	// Explicit re-save of exactly that version.
	stdout, code, stderr = runCLI(t, "template", "audit", "--target", target, "--rewrite", oldVersion)
	if code != 0 {
		t.Fatalf("template audit --rewrite: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, oldVersion) {
		t.Fatalf("the rewrite must name the re-saved version: %s", stdout)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the rewrite must write the trusted file anchor: %v", err)
	}
	activeAfter, err := os.ReadFile(activePath)
	if err != nil || string(activeAfter) != string(activeBefore) {
		t.Fatalf("template audit must not change the file config: %q, %v", activeAfter, err)
	}

	// The re-saved version loads; the audit no longer reports it.
	reg := agent.NewFS(os.DirFS("../../agents"))
	if _, _, err := loadPinnedResumeConfig(target, "run-old", reg); err != nil {
		t.Fatalf("the re-saved version must be runnable: %v", err)
	}
	stdout, code, stderr = runCLI(t, "template", "audit", "--target", target)
	if code != 0 {
		t.Fatalf("template audit after rewrite: code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, oldVersion) {
		t.Fatalf("a re-saved version must not be a finding: %s", stdout)
	}
}

func TestTemplateAuditHelpAndDispatch(t *testing.T) {
	stdout, code, stderr := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("--help: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "ai-team template audit") {
		t.Fatalf("--help must document template audit: %s", stdout)
	}

	// No subcommand and an unknown subcommand are usage errors.
	for _, args := range [][]string{{"template"}, {"template", "nope"}} {
		_, code, stderr := runCLI(t, args...)
		if code == 0 || !strings.Contains(stderr, "template") {
			t.Fatalf("%v: expected a template usage error, code=%d stderr=%s", args, code, stderr)
		}
	}
}
