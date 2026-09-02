package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/redact"
)

// TestRedactVerifyBlocksOnSecrets — интеграционная проверка fail-closed
// политики (P1-6): verify над evidence run'а при секрете возвращает ошибку.
func TestRedactVerifyBlocksOnSecrets(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, ".ai-team", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"),
		[]byte("{\"prompt\":\"token="+"ghp_"+strings.Repeat("4", 36)+"\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	policy := redact.Policy{FailOnSecrets: true}
	if _, err := redact.Verify(runDir, policy); err == nil {
		t.Fatal("fail-closed verify должен вернуть ошибку при секрете")
	}
}

// TestExportRefusesSecretsUsesPolicy — блокирующая логика экспорта держится
// на Verified политике: при disable_export_block скан позволяет экспорт.
func TestExportRefusesSecretsUsesPolicy(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, ".ai-team", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"),
		[]byte("password=Yv3sM3Yl0n9WxQ2aB1cD4eF6g\n"), 0644); err != nil {
		t.Fatal(err)
	}

	policy := redact.Policy{FailOnSecrets: false}
	report, err := redact.Verify(runDir, policy)
	if err != nil {
		t.Fatalf("с FailOnSecrets=false verify не должен фейлить: %v", err)
	}
	if report == nil || len(report.Violations) == 0 {
		t.Fatal("ожидалась зарегистрированная violation при выключенном блокере")
	}
}

// TestRedactCommandRedactsHappyPath — CLI-путь `redact redact` через
// applyRedact (Policy.MaxBytes==0 по умолчанию) должен работать: файл с
// секретом даёт redaction-копию, чистый файл копируется без изменений.
func TestRedactCommandRedactsHappyPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "secret.txt"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("1", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "clean.txt"),
		[]byte("обычный текст\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "redacted")
	if err := applyRedact(source, out, redact.Policy{}); err != nil {
		t.Fatalf("applyRedact: %v", err)
	}

	secretOut, err := os.ReadFile(filepath.Join(out, "secret.txt"))
	if err != nil {
		t.Fatalf("ожидалась redaction-копия secret.txt: %v", err)
	}
	if strings.Contains(string(secretOut), "ghp_") {
		t.Errorf("секрет не был заменён в копии: %q", secretOut)
	}
	if !strings.Contains(string(secretOut), "[REDACTED:github token]") {
		t.Errorf("ожидался маркер замены, got: %q", secretOut)
	}
	cleanOut, err := os.ReadFile(filepath.Join(out, "clean.txt"))
	if err != nil {
		t.Fatalf("ожидалась копия clean.txt: %v", err)
	}
	if string(cleanOut) != "обычный текст\n" {
		t.Errorf("чистый файл должен копироваться без изменений, got %q", cleanOut)
	}
}

// TestApplyRedactRejectsOutInsideSource — SF4: --out внутри источника должен
// отклоняться сразу (иначе WalkDir спиралит по создаваемой копии).
func TestApplyRedactRejectsOutInsideSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "x.txt"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("2", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(source, "redacted")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	if err := applyRedact(source, out, redact.Policy{}); err == nil {
		t.Fatal("applyRedact должен вернуть ошибку при out внутри source")
	}
}
