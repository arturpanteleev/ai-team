package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func testUsageEnvelope(runID string) UsageEnvelope {
	started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	return Build(runID, "feature", started, started.Add(time.Second), []workflow.StageResult{
		{Name: "coder", Duration: time.Second},
	}, 0, "completed", Usage{})
}

func TestFileUsageEnvelopeStoreWriteReadAndImmutableRetry(t *testing.T) {
	target := t.TempDir()
	envelope := testUsageEnvelope("usage-run")
	store := FileUsageEnvelopeStore{}
	if err := store.Reserve(target, envelope.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(target, envelope.RunID, envelope); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(target, envelope.RunID, envelope); err != nil {
		t.Fatalf("exact write retry should be idempotent: %v", err)
	}
	read, err := ReadUsageEnvelope(target, envelope.RunID)
	if err != nil || read.RunID != envelope.RunID || read.Feature != envelope.Feature {
		t.Fatalf("read envelope = %+v, %v", read, err)
	}
	changed := envelope
	changed.Outcome = "failed"
	if err := store.Write(target, envelope.RunID, changed); err == nil {
		t.Fatal("different envelope must not replace immutable usage state")
	}
}

func TestFileUsageEnvelopeStoreRejectsIdentityAndCorruption(t *testing.T) {
	target := t.TempDir()
	store := FileUsageEnvelopeStore{}
	envelope := testUsageEnvelope("expected")
	if err := store.Reserve(target, envelope.RunID); err != nil {
		t.Fatal(err)
	}
	wrong := envelope
	wrong.RunID = "other"
	if err := store.Write(target, envelope.RunID, wrong); err == nil {
		t.Fatal("mismatched run identity must fail")
	}
	if _, err := PrepareUsageEnvelopeStore(target); err != nil {
		t.Fatal(err)
	}
	path, err := UsageEnvelopePath(target, envelope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"run_id":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUsageEnvelope(target, envelope.RunID); err == nil {
		t.Fatal("corrupt or mismatched stored envelope must fail closed")
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}

func TestFileUsageEnvelopeStoreCanAdvanceOnRecovery(t *testing.T) {
	target := t.TempDir()
	store := FileUsageEnvelopeStore{}
	first := testUsageEnvelope("recover-run")
	if err := store.Reserve(target, first.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(target, first.RunID, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Outcome = "completed-after-recovery"
	second.FinishedAt = second.FinishedAt.Add(time.Minute)
	if err := store.Replace(target, second.RunID, second); err != nil {
		t.Fatalf("recovery should be able to advance final summary: %v", err)
	}
	stored, err := ReadUsageEnvelope(target, second.RunID)
	if err != nil || stored.Outcome != second.Outcome || !stored.FinishedAt.Equal(second.FinishedAt) {
		t.Fatalf("recovered summary = %+v, %v", stored, err)
	}
}

func TestUsageReservationIsPerRunAndCorruptionFailsClosed(t *testing.T) {
	target := t.TempDir()
	store := FileUsageEnvelopeStore{}
	if err := store.Reserve(target, "reserved-run"); err != nil {
		t.Fatal(err)
	}
	reserved, err := UsageEnvelopeReservation(target, "reserved-run")
	if err != nil || !reserved {
		t.Fatalf("reservation lookup = %v, %v", reserved, err)
	}
	other, err := UsageEnvelopeReservation(target, "legacy-run")
	if err != nil || other {
		t.Fatalf("unrelated reservation must not select controller state: %v, %v", other, err)
	}
	path, err := UsageEnvelopePath(target, "reserved-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".json")+".reserved.json", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := UsageEnvelopeReservation(target, "reserved-run"); err == nil {
		t.Fatal("corrupt reservation must fail closed")
	}
}
