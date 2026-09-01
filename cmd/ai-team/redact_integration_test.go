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
