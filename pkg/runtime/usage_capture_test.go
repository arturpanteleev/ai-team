package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCLI кладёт исполняемый sh-скрипт с именем харнесса (codex/claude)
// в отдельный каталог и возвращает путь к нему.
func writeFakeCLI(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// noiseJSONL возвращает путь к файлу с валидными JSONL-событиями суммарно не
// меньше size байт: имитирует болтливый (дорогой) прогон харнесса.
func noiseJSONL(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "noise.jsonl")
	var buf bytes.Buffer
	pad := strings.Repeat("x", 512)
	for i := 0; buf.Len() < size; i++ {
		fmt.Fprintf(&buf, "{\"type\":\"item.completed\",\"item\":{\"id\":%d,\"text\":\"%s\"}}\n", i, pad)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runFakeHarness прогоняет фейковый харнесс через AgentCLIRuntime и
// возвращает runtime, размер вывода и содержимое лога агента.
func runFakeHarness(t *testing.T, cli string) (*AgentCLIRuntime, int, string) {
	t.Helper()
	target := t.TempDir()
	logDir := t.TempDir()
	r := &AgentCLIRuntime{}
	var console bytes.Buffer
	task := &Task{
		Feature: "feat", TaskDesc: "d", TargetDir: target, LogDir: logDir,
		ArtifactRoot: filepath.Join(target, ".ai-team", "artifacts"),
		ConsoleOut:   &console,
	}
	if err := r.Execute(context.Background(), &Agent{Name: "coder", CLI: cli, Prompt: "p"}, task, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(logDir, "coder.log"))
	if err != nil {
		t.Fatalf("лог агента: %v", err)
	}
	return r, console.Len(), string(log)
}

func TestExecuteParsesCodexUsageOnSmallOutput(t *testing.T) {
	cli := writeFakeCLI(t, "codex", "cat >/dev/null\n"+
		`printf '{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":200}}\n'`+"\n")
	r, size, _ := runFakeHarness(t, cli)
	t.Logf("stdout=%8d B → usage=%+v", size, r.Usage())
	if r.Usage() == nil {
		t.Fatal("usage должен быть разобран на коротком выводе")
	}
	if r.UsageError() != nil {
		t.Fatalf("разобранный usage не должен нести ошибку: %v", r.UsageError())
	}
}

func TestExecuteParsesCodexUsageBeyondCaptureLimit(t *testing.T) {
	noise := noiseJSONL(t, 3<<20)
	cli := writeFakeCLI(t, "codex", "cat >/dev/null\ncat "+noise+"\n"+
		`printf '{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":10,"output_tokens":200}}\n'`+"\n")
	r, size, _ := runFakeHarness(t, cli)
	usage := r.Usage()
	t.Logf("stdout=%8d B → usage=%+v", size, usage)
	if usage == nil {
		t.Fatalf("usage потерян на выводе %d B: %v", size, r.UsageError())
	}
	if usage.TokensInput != 1000 || usage.TokensOutput != 200 || usage.CachedInputTokens != 10 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestExecuteParsesClaudeUsageBeyondCaptureLimit(t *testing.T) {
	noise := noiseJSONL(t, 3<<20)
	cli := writeFakeCLI(t, "claude", "cat >/dev/null\ncat "+noise+"\n"+
		`printf '{"type":"result","total_cost_usd":1.25,"usage":{"input_tokens":1000,"output_tokens":200}}\n'`+"\n")
	r, size, _ := runFakeHarness(t, cli)
	usage := r.Usage()
	t.Logf("stdout=%8d B → usage=%+v", size, usage)
	if usage == nil || usage.CostUSD != 1.25 {
		t.Fatalf("usage потерян/искажён на выводе %d B: %+v (%v)", size, usage, r.UsageError())
	}
}

// Вывод сверх лимита кап-буфера не должен превращать успешный прогон в
// провал: до QS-20 короткая запись буфера давала io.ErrShortWrite, и агент
// «падал» с чужой ошибкой (ClassifyError выдавал её за отказ аутентификации).
func TestExecuteSurvivesOutputBeyondCaptureLimit(t *testing.T) {
	noise := noiseJSONL(t, 6<<20)
	cli := writeFakeCLI(t, "codex", "cat >/dev/null\ncat "+noise+"\n"+
		`printf '{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}\n'`+"\n")
	for i := 0; i < 3; i++ {
		r, size, _ := runFakeHarness(t, cli)
		if r.Usage() == nil || r.Usage().TokensInput != 7 {
			t.Fatalf("прогон #%d (stdout=%d B): usage=%+v err=%v", i, size, r.Usage(), r.UsageError())
		}
	}
}

// Ненайденная usage-запись обязана быть заметна: Usage()=nil больше не
// единственный след — есть причина и строка в логе агента.
func TestExecuteReportsMissingUsageRecord(t *testing.T) {
	noise := noiseJSONL(t, 3<<20)
	cli := writeFakeCLI(t, "codex", "cat >/dev/null\ncat "+noise+"\n")
	r, size, log := runFakeHarness(t, cli)
	if r.Usage() != nil {
		t.Fatalf("usage не мог быть разобран: %+v", r.Usage())
	}
	usageErr := r.UsageError()
	t.Logf("stdout=%8d B → usage=nil, причина: %v", size, usageErr)
	if usageErr == nil {
		t.Fatal("отсутствие usage-записи обязано нести причину, а не молчаливый nil")
	}
	if !strings.Contains(usageErr.Error(), "turn.completed") {
		t.Fatalf("причина должна называть, чего не нашлось: %v", usageErr)
	}
	if !strings.Contains(log, "расход агента coder не учтён") {
		t.Fatalf("пропуск в учёте расхода обязан попасть в лог агента:\n%s", lastLines(log, 5))
	}
}

// Голова вывода (ранние фатальные ошибки харнесса) остаётся доступной
// классификатору и на выводе сверх лимита.
func TestExecuteClassifiesErrorFromHeadOfOversizedOutput(t *testing.T) {
	noise := noiseJSONL(t, 3<<20)
	cli := writeFakeCLI(t, "claude", "cat >/dev/null\n"+
		`printf 'fatal: invalid api key\n'`+"\ncat "+noise+"\nexit 1\n")
	target := t.TempDir()
	r := &AgentCLIRuntime{}
	var console bytes.Buffer
	task := &Task{Feature: "feat", TaskDesc: "d", TargetDir: target,
		ArtifactRoot: filepath.Join(target, ".ai-team", "artifacts"), ConsoleOut: &console}
	err := r.Execute(context.Background(), &Agent{Name: "coder", CLI: cli, Prompt: "p"}, task, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка прогона")
	}
	if !strings.Contains(err.Error(), "аутентификация") {
		t.Fatalf("ранняя ошибка из головы вывода потеряна: %v", err)
	}
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
