package delivery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// terminal_test.go — QS-23: record_sha256 объявлен self-integrity digest'ом,
// но был необязательным: удаление строки обходило проверку целиком.

func sampleRecord() TerminalRecord {
	return TerminalRecord{
		SchemaVersion: TerminalRecordSchemaVersion,
		RunID:         "run-1", Feature: "feat",
		PlanHash:  strings.Repeat("ab", 32),
		CommitSHA: strings.Repeat("1a", 20),
		PRURL:     "https://example.test/pr/1",
		Trailers:  []string{TrailerRunID + ": run-1"},
		// PerformedAt в UTC: селф-digest считается по canonical-сериализации.
		PerformedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func TestWriteAndReadTerminalRecordRoundtrip(t *testing.T) {
	runDir := t.TempDir()
	if err := WriteTerminalRecord(runDir, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	record, ok, err := ReadTerminalRecord(runDir)
	if err != nil || !ok {
		t.Fatalf("record должен читаться: ok=%v err=%v", ok, err)
	}
	if record.RecordSHA256 == "" {
		t.Fatal("писатель обязан проставлять record_sha256")
	}
}

func TestReadTerminalRecordRequiresSelfDigest(t *testing.T) {
	runDir := t.TempDir()
	if err := WriteTerminalRecord(runDir, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, "delivery.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["pr_url"] = "https://evil.test/pr/1"
	delete(document, "record_sha256")
	patched, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ReadTerminalRecord(runDir); err == nil {
		t.Fatal("record без record_sha256 обязан отвергаться")
	} else if !strings.Contains(err.Error(), "record_sha256") {
		t.Fatalf("ожидалась причина про record_sha256, получено: %v", err)
	}
	if _, _, err := ReadTerminalRecordFile(path); err == nil {
		t.Fatal("ReadTerminalRecordFile обязан отвергать record без record_sha256")
	}
}

func TestReadTerminalRecordDetectsEditedFields(t *testing.T) {
	runDir := t.TempDir()
	if err := WriteTerminalRecord(runDir, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, "delivery.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), "https://example.test/pr/1", "https://evil.test/pr/1", 1)
	if tampered == string(data) {
		t.Fatal("подмена не применилась")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTerminalRecord(runDir); err == nil {
		t.Fatal("правка pr_url обязана ломать self-integrity digest")
	}
}
