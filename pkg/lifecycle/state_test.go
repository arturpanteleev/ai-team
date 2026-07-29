package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validState(target string) State {
	return State{
		RunID: "run-1", Feature: "feature", TargetDir: target, Task: "задача",
		Phase: PhaseRunning, NextStage: "analyst", ConfigSHA256: string(make([]byte, 64)),
		WorkflowSHA256: string(make([]byte, 64)), CreatedAt: time.Now().UTC(),
	}
}

func TestStoreLifecycleTransitions(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := validState(target)
	if err := store.Create(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	next := loaded
	next.Phase = PhaseResumable
	if err := store.Save(loaded, next); err != nil {
		t.Fatal(err)
	}
	resumable, _ := store.Load(state.RunID)
	running := resumable
	running.Phase = PhaseRunning
	if err := store.Save(resumable, running); err != nil {
		t.Fatal(err)
	}
	terminal := running
	terminal.Phase, terminal.NextStage = PhaseTerminal, ""
	if err := store.Save(running, terminal); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(terminal, running); err == nil {
		t.Fatal("terminal state не должен возобновляться")
	}
}

func TestStoreRejectsCorruptionAndSymlink(t *testing.T) {
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, ".ai-team", "state", "runs", "run-1.json")
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("run-1"); err == nil {
		t.Fatal("повреждённый state должен быть отклонён")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(target, "actual")
	if err := os.WriteFile(actual, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, path); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(validState(target)); err == nil {
		t.Fatal("symlink state path должен быть отклонён")
	}
}
