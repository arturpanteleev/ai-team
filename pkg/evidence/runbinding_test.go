package evidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyChainGenesis — общий для всех прогонов корень цепочки до QS-13.
// Подделка в тесте строится именно от него: так кейс остаётся воспроизводимым
// на коде до фикса и не зависит от текущей реализации chainGenesis.
const legacyChainGenesis = "0000000000000000000000000000000000000000000000000000000000000000"

// buildRunWithEvents создаёт прогон с непустым non-terminal lifecycle-логом.
func buildRunWithEvents(t *testing.T, root, runID string) *Store {
	t.Helper()
	store, err := Start(root, testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Append(Event{Type: "run_started", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{
		Type: "attempt_started", Stage: "analyst", AttemptID: runID + "-001-analyst",
		Timestamp: now.Add(time.Millisecond), Data: map[string]any{"stage_index": 1},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// restampEventLog переклеивает чужой лог под указанный run_id: правит поле в
// каждом событии и пересобирает всю hash-chain от legacy-корня. Ровно то, что
// может сделать кто угодно: секрета в цепочке нет, а дайджест каждого события
// считается по его собственному run_id.
func restampEventLog(t *testing.T, data []byte, runID string) []byte {
	t.Helper()
	previous := legacyChainGenesis
	var rebuilt bytes.Buffer
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		event.RunID = runID
		event.PreviousSHA256 = previous
		digest, err := eventDigest(event)
		if err != nil {
			t.Fatal(err)
		}
		event.SHA256 = digest
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt.Write(encoded)
		rebuilt.WriteByte('\n')
		previous = digest
	}
	return rebuilt.Bytes()
}

// QS-13: лог обязан быть привязан к своему прогону. Целый валидный лог другого
// прогона, положенный в каталог этого, не должен проходить проверку целостности
// — ни как есть, ни перештампованный под чужой run_id.
func TestEventLogIsBoundToItsRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	storeA := buildRunWithEvents(t, root, "run-victim")
	storeB := buildRunWithEvents(t, root, "run-foreign")

	logPathA := filepath.Join(storeA.RunDir(), "events.jsonl")
	logB, err := os.ReadFile(filepath.Join(storeB.RunDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if events, err := VerifyEventLog(logPathA, "run-victim"); err != nil || len(events) != 2 {
		t.Fatalf("собственный лог обязан проходить: events=%d err=%v", len(events), err)
	}

	t.Run("чужой лог как есть", func(t *testing.T) {
		if err := os.WriteFile(logPathA, logB, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyEventLog(logPathA, "run-victim"); err == nil {
			t.Fatal("лог чужого прогона принят как валидная evidence run-victim")
		}
	})

	t.Run("чужой лог, перештампованный под run-victim", func(t *testing.T) {
		if err := os.WriteFile(logPathA, restampEventLog(t, logB, "run-victim"), 0o644); err != nil {
			t.Fatal(err)
		}
		events, err := VerifyEventLog(logPathA, "run-victim")
		if err == nil {
			t.Fatalf("перештампованный чужой лог принят как валидная evidence run-victim: events=%d", len(events))
		}
		if !strings.Contains(err.Error(), "не принадлежит run run-victim") {
			t.Fatalf("ожидалась диагностика о привязке к прогону, получено: %v", err)
		}
	})

	t.Run("resume отвергает перештампованный чужой лог", func(t *testing.T) {
		if err := os.WriteFile(logPathA, restampEventLog(t, logB, "run-victim"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Non-terminal run: anchor.json ещё нет, и привязка лога к прогону —
		// единственное, что отделяет историю run-foreign от истории run-victim.
		if _, _, _, err := Resume(root, "run-victim"); err == nil {
			t.Fatal("Resume принял историю чужого прогона как свою")
		}
	})
}
