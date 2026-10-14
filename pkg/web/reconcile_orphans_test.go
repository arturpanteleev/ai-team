package web

import (
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func TestReconcileOrphanRunsRespectsWorkspaceLock(t *testing.T) {
	target := t.TempDir()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	started := time.Now().UTC()
	recorder := &StoreRecorder{store: st}
	recorder.RunStarted("run-orphan", "feature", "", started)

	srv := &Server{store: st, targetDir: target}

	held, err := evidence.AcquireWorkspaceLock(target)
	if err != nil {
		t.Fatal(err)
	}
	srv.reconcileOrphanRuns(started.Add(time.Minute))
	if status := runStatus(t, st, "run-orphan"); status != "running" {
		t.Fatalf("run с живым lock не должен меняться, status=%q", status)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}

	srv.reconcileOrphanRuns(started.Add(2 * time.Minute))
	if status := runStatus(t, st, "run-orphan"); status != "interrupted" {
		t.Fatalf("осиротевший run должен стать interrupted, status=%q", status)
	}
}

func runStatus(t *testing.T, st *store.Store, runID string) string {
	t.Helper()
	runs, err := st.GetPipelineRuns()
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.RunID == runID {
			return run.Status
		}
	}
	t.Fatalf("run %s не найден", runID)
	return ""
}
