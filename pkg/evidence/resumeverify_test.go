package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildNonTerminalRun создаёт активный (не-терминальный) run: run_started +
// attempt_started, без run_finished и без anchor.
func buildNonTerminalRun(t *testing.T, runID string) string {
	t.Helper()
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: runID + "-001-check",
		Timestamp: now.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	return store.RunDir()
}

func TestVerifyResumeEvidencePassesOnActiveRun(t *testing.T) {
	runDir := buildNonTerminalRun(t, "run-resume-ok")
	if err := VerifyResumeEvidence(runDir); err != nil {
		t.Fatalf("активный run должен пройти fail-closed проверку: %v", err)
	}
}

func TestVerifyResumeEvidenceDetectsConfigTamper(t *testing.T) {
	runDir := buildNonTerminalRun(t, "run-resume-config")
	configPath := filepath.Join(runDir, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config.json expected at %s: %v", configPath, err)
	}
	if err := os.Chmod(configPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schema_version":1,"galtered":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	err := VerifyResumeEvidence(runDir)
	var rv *ResumeEvidenceError
	if !errors.As(err, &rv) || rv.Reason != ReasonConfigSnapshot {
		t.Fatalf("ожидали ReasonConfigSnapshot, получили %v", err)
	}
}

func TestVerifyResumeEvidenceDetectsEventChainTamper(t *testing.T) {
	runDir := buildNonTerminalRun(t, "run-resume-chain")
	// подменяем run_started в середине — hash chain сломается
	path := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "run_started" {
			ev.Data = map[string]any{"tampered": true}
			encoded, _ := json.Marshal(ev)
			lines[i] = string(encoded)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	err = VerifyResumeEvidence(runDir)
	var rv *ResumeEvidenceError
	if !errors.As(err, &rv) || rv.Reason != ReasonEventChain {
		t.Fatalf("ожидали ReasonEventChain, получили %v", err)
	}
}

func TestVerifyResumeEvidenceRejectsTerminalRun(t *testing.T) {
	// buildAnchoredRun создаёт терминальный run
	runDir := buildAnchoredRun(t, "run-resume-terminal")
	err := VerifyResumeEvidence(runDir)
	var rv *ResumeEvidenceError
	if !errors.As(err, &rv) || rv.Reason != ReasonAlreadyTerminal {
		t.Fatalf("ожидали ReasonAlreadyTerminal, получили %v", err)
	}
	cause := rv.Unwrap()
	if !errors.Is(err, ErrResumeEvidence) || !errors.Is(cause, ErrResumeEvidence) {
		t.Fatalf("должен быть сентинель ErrResumeEvidence")
	}
}
