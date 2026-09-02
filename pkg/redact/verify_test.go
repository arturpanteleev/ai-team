package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyAppliesIncludeExclude(t *testing.T) {
	p := Policy{
		Include: []string{"runs/**"},
		Exclude: []string{"**/reports/**"},
	}
	if !p.Applies("runs/abc/events.jsonl") {
		t.Error("runs/** должно включать events.jsonl")
	}
	if p.Applies("reports/foo.html") {
		t.Error("вне include должен быть исключён")
	}
	if p.Applies("runs/abc/reports/x.html") {
		t.Error("exclude **/reports/** должен исключать")
	}
}

func TestPolicyValidateRejectsTraversal(t *testing.T) {
	p := Policy{Include: []string{"../escape"}}
	if err := p.Validate(); err == nil {
		t.Error("glob с выходом из workspace должен быть отвергнут")
	}
}

func TestVerifyFailClosedOnSecrets(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755)
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("2", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := Policy{FailOnSecrets: true}
	report, err := Verify(dir, p)
	if err == nil {
		t.Fatalf("fail-closed: ожидалась ошибка, report=%+v", report)
	}
	if report == nil || len(report.Violations) != 1 {
		t.Fatalf("ожидалась 1 violation, got %+v", report)
	}
	if report.Violations[0].Path != "runs/x/events.jsonl" {
		t.Fatalf("путь нарушения: %s", report.Violations[0].Path)
	}
}

func TestVerifyCleanPasses(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755)
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"),
		[]byte("{\"stage\":\"review\",\"verdict\":\"APPROVED\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: true})
	if err != nil {
		t.Fatalf("чистый каталог не должен фейлить: %v", err)
	}
	if report.Verdict != "clean" {
		t.Fatalf("verdict = %s", report.Verdict)
	}
}

func TestVerifyExcludeSkipsFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "meta"), 0755)
	if err := os.WriteFile(filepath.Join(dir, "meta", "tokens.txt"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("3", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := Policy{FailOnSecrets: true, Exclude: []string{"meta/**"}}
	if _, err := Verify(dir, p); err != nil {
		t.Fatalf("excluded-файл должен быть проигнорирован: %v", err)
	}
}
