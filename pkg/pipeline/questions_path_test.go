package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuestionsPayloadFromPathReadsStageQuestionsFile(t *testing.T) {
	root := t.TempDir()
	path := stageQuestionsPath(root, "feat")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("1. Какой домен допускается?"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := questionsPayloadFromPath(path)
	if err != nil || !ok {
		t.Fatalf("вопросы в стандартном пути должны читаться: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(payload), "домен") {
		t.Fatalf("payload без текста вопросов: %s", payload)
	}
	if _, ok, err := questionsPayloadFromPath(filepath.Join(root, "missing.md")); ok || err != nil {
		t.Fatalf("отсутствующий файл не должен считаться вопросами: ok=%v err=%v", ok, err)
	}
}
