package evidence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

// TestFindDeliveredSkipsRunWithBrokenDeliveryRecord — QS-23: ошибка чтения
// канонического delivery.json раньше молча отбрасывалась, и результат брался
// из более слабого источника (attempt-манифеста). Сломанный record означает
// «про этот run ничего не известно», а не «возьмём другой источник».
func TestFindDeliveredSkipsRunWithBrokenDeliveryRecord(t *testing.T) {
	target := t.TempDir()
	runsRoot := filepath.Join(target, "runs")
	artifactRoot := filepath.Join(target, "artifacts")

	manifest := testRunManifest("run-broken-delivery")
	manifest.Feature = "add-jwt-auth"
	store, err := Start(runsRoot, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-1", Stage: "deployer", Status: "completed",
		Delivery: &delivery.Result{CommitSHA: "deadbeef", PRURL: "https://example.invalid/pr/1"},
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}
	// delivery.json без self-integrity digest — именно та форма подделки, что
	// проходила раньше.
	broken := `{"schema_version":1,"run_id":"run-broken-delivery","feature":"add-jwt-auth",` +
		`"plan_hash":"` + hex64String() + `","pr_url":"https://evil.test/pr/1","performed_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(store.RunDir(), "delivery.json"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := FindDelivered(runsRoot, "add-jwt-auth")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("run со сломанным delivery.json не должен считаться доставленным")
	}
}

func hex64String() string {
	value := make([]byte, 64)
	for i := range value {
		value[i] = 'a'
	}
	return string(value)
}
