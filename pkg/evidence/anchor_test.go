package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildAnchoredRun создаёт терминальный run с attempt manifest и anchor.json.
func buildAnchoredRun(t *testing.T, runID string) string {
	t.Helper()
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	attemptID := runID + "-001-check"
	steps := []func() error{
		func() error { return store.Append(Event{Type: "run_started", Timestamp: started}) },
		func() error {
			return store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: attemptID,
				Timestamp: started.Add(time.Second), Data: map[string]any{"stage_index": 1}})
		},
		func() error {
			return store.PublishAttempt(AttemptManifest{
				AttemptID: attemptID, Stage: "check", StageIndex: 1,
				StartedAt: started.Add(time.Second), FinishedAt: started.Add(2 * time.Second),
				Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
			}, filepath.Join(target, "artifacts"), nil, nil)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	_, _, manifestDigest, err := ArtifactDigest(filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: attemptID,
		Timestamp: started.Add(2 * time.Second), Data: map[string]any{
			"status": "passed", "execution": "succeeded", "decision": "approved",
			"outcome": "passed", "manifest_sha256": manifestDigest,
		}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "run_finished", Timestamp: started.Add(3 * time.Second),
		Data: map[string]any{"status": "completed", "stage_attempts": 1}}); err != nil {
		t.Fatal(err)
	}
	return store.RunDir()
}

func readAnchor(t *testing.T, runDir string) Anchor {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "anchor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var anchor Anchor
	if err := json.Unmarshal(data, &anchor); err != nil {
		t.Fatal(err)
	}
	return anchor
}

func TestTerminalEventWritesValidAnchor(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-ok")
	anchor := readAnchor(t, runDir)
	events, err := VerifyEventLog(filepath.Join(runDir, "events.jsonl"), "run-anchor-ok")
	if err != nil || len(events) == 0 {
		t.Fatalf("event log must be valid: err=%v len=%d", err, len(events))
	}
	if anchor.SchemaVersion != AnchorSchemaVersion || anchor.RunID != "run-anchor-ok" ||
		anchor.TerminalEvent != "run_finished" || anchor.EventCount != uint64(len(events)) ||
		anchor.ChainRootSHA256 != events[len(events)-1].SHA256 || anchor.CreatedAt.IsZero() {
		t.Fatalf("unexpected anchor: %+v", anchor)
	}
	if err := VerifyAnchor(runDir); err != nil {
		t.Fatalf("VerifyAnchor должен проходить на нетронутом run: %v", err)
	}
}

func TestVerifyAnchorDetectsTamperedMiddleEvent(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-mid")
	path := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"type":"attempt_started"`, `"type":"attempt_hacked"`, 1)
	if tampered == string(data) {
		t.Fatal("подмена не применилась")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	err = VerifyAnchor(runDir)
	if err == nil {
		t.Fatal("подмена события в середине должна ломать VerifyAnchor")
	}
	if !strings.Contains(err.Error(), "tampering") && !strings.Contains(err.Error(), "сломан") {
		t.Fatalf("ожидалась причина tampering, получено: %v", err)
	}
}

func TestVerifyAnchorDetectsRemovedLastEvent(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-trunc")
	path := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("слишком мало событий: %d", len(lines))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAnchor(runDir); err == nil {
		t.Fatal("удаление последнего события должно ломать VerifyAnchor")
	}
}

// Ключевой кейс: злоумышленник пересобирает цепочку целиком (пересчитывает
// previous_sha256/sha256 после подмены), но anchor.json остался от исходной
// цепочки — VerifyAnchor обязан это обнаружить по chain_root_sha256.
func TestVerifyAnchorDetectsRebuiltChainWithoutAnchorUpdate(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-rebuild")
	path := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	rebuilt := make([]string, 0, len(lines))
	previous := genesisEventHash
	for index, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		event.PreviousSHA256 = previous
		// Подмена содержимого середины цепочки + полный пересчёт хешей.
		if index == 1 {
			event.Timestamp = event.Timestamp.Add(500 * time.Millisecond)
		}
		digest, err := eventDigest(event)
		if err != nil {
			t.Fatal(err)
		}
		event.SHA256 = digest
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt = append(rebuilt, string(encoded))
		previous = digest
	}
	if err := os.WriteFile(path, []byte(strings.Join(rebuilt, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayEventLog(path, "run-anchor-rebuild"); err != nil {
		t.Fatalf("пересобранная цепочка должна проходить replay: %v", err)
	}
	err = VerifyAnchor(runDir)
	if err == nil {
		t.Fatal("пересобранная цепочка без обновления anchor должна ломать VerifyAnchor")
	}
	if !strings.Contains(err.Error(), "chain_root_sha256") {
		t.Fatalf("ожидалась ошибка chain root, получено: %v", err)
	}
}

func TestVerifyAnchorRejectsMissingOrCorruptAnchor(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-missing")
	if err := os.Remove(filepath.Join(runDir, "anchor.json")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAnchor(runDir); err == nil {
		t.Fatal("отсутствие anchor.json должно быть ошибкой")
	}

	runDir = buildAnchoredRun(t, "run-anchor-corrupt")
	anchorPath := filepath.Join(runDir, "anchor.json")
	data, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(data), `"event_count":`, `"event_count_x":`, 1)
	if err := os.WriteFile(anchorPath, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAnchor(runDir); err == nil {
		t.Fatal("повреждённый anchor.json должен быть ошибкой")
	}
}
