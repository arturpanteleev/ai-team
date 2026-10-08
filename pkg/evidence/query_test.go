package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

func TestFindDeliveredLocatesSuccessfulDeployerAttempt(t *testing.T) {
	target := t.TempDir()
	runsRoot := filepath.Join(target, "runs")
	artifactRoot := filepath.Join(target, "artifacts")

	manifest := testRunManifest("run-delivered")
	manifest.Feature = "add-jwt-auth"
	store, err := Start(runsRoot, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-1",
		Stage:     "deployer",
		Status:    "completed",
		Delivery:  &delivery.Result{CommitSHA: "deadbeef", PRURL: "https://example.invalid/pr/1"},
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}

	found, ok, err := FindDelivered(runsRoot, "add-jwt-auth")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.RunID != "run-delivered" || found.Delivery.CommitSHA != "deadbeef" {
		t.Fatalf("ожидалась найденная delivery: found=%+v ok=%v", found, ok)
	}
}

func TestFindDeliveredIgnoresOtherFeaturesAndIncompleteRuns(t *testing.T) {
	target := t.TempDir()
	runsRoot := filepath.Join(target, "runs")
	artifactRoot := filepath.Join(target, "artifacts")

	otherFeature := testRunManifest("run-other-feature")
	otherFeature.Feature = "unrelated-feature"
	storeOther, err := Start(runsRoot, otherFeature)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeOther.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-1",
		Stage:     "deployer",
		Delivery:  &delivery.Result{CommitSHA: "abc"},
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}

	notYetDelivered := testRunManifest("run-not-delivered")
	notYetDelivered.Feature = "add-jwt-auth"
	storeNotDelivered, err := Start(runsRoot, notYetDelivered)
	if err != nil {
		t.Fatal(err)
	}
	// deployer этап дошёл до attempt, но ещё не выполнил delivery (например,
	// stopped перед --approve-plan) — Delivery остаётся nil.
	if err := storeNotDelivered.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-1",
		Stage:     "deployer",
		Status:    "stopped",
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Второй attempt того же run: Delivery не nil (план построен, шаги
	// записаны), но commit/push не произошли — CommitSHA и PRURL пусты. Это
	// не должно засчитываться как delivered.
	if err := storeNotDelivered.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-2",
		Stage:     "deployer",
		Status:    "failed",
		Delivery:  &delivery.Result{PlanHash: "planhash", Steps: []delivery.StepResult{{Step: "verify_clean_index"}}},
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}

	found, ok, err := FindDelivered(runsRoot, "add-jwt-auth")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("не должно быть найдено delivery для add-jwt-auth: %+v", found)
	}
}

func TestFindDeliveredOnMissingRunsRootReturnsNotFound(t *testing.T) {
	found, ok, err := FindDelivered(filepath.Join(t.TempDir(), "does-not-exist"), "any-feature")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("ожидалось not-found, получено: %+v", found)
	}
}

func TestFindDeliveredDoesNotSwallowCorruptTerminalRecord(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	manifest := testRunManifest("run-corrupt-delivery")
	manifest.Feature = "feature"
	store, err := Start(runsRoot, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.WriteTerminalRecord(store.RunDir(), delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion,
		RunID:         manifest.RunID, Feature: manifest.Feature,
		PlanHash: strings.Repeat("a", 64), CommitSHA: strings.Repeat("b", 40),
		PerformedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.RunDir(), "delivery.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), strings.Repeat("b", 40), strings.Repeat("c", 40), 1)
	if err := os.WriteFile(path, []byte(tampered), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FindDelivered(runsRoot, manifest.Feature); err == nil {
		t.Fatal("FindDelivered swallowed corrupt delivery evidence")
	}
}

// Имя delivery-стадии задаётся пользователем в конфиге: workflow может назвать
// её `ship` вместо `deployer`. Запись о доставке должна находиться в обоих
// случаях — evidence-слой не знает имён ролей.
func TestFindDeliveredLocatesCustomNamedDeliveryStage(t *testing.T) {
	target := t.TempDir()
	runsRoot := filepath.Join(target, "runs")
	artifactRoot := filepath.Join(target, "artifacts")

	manifest := testRunManifest("run-ship")
	manifest.Feature = "add-jwt-auth"
	store, err := Start(runsRoot, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: "attempt-1",
		Stage:     "ship",
		Status:    "completed",
		Delivery:  &delivery.Result{CommitSHA: "cafebabe", PRURL: "https://example.invalid/pr/7"},
	}, artifactRoot, nil, nil); err != nil {
		t.Fatal(err)
	}

	found, ok, err := FindDelivered(runsRoot, "add-jwt-auth")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.RunID != "run-ship" || found.Delivery.CommitSHA != "cafebabe" {
		t.Fatalf("ожидалась найденная delivery для стадии ship: found=%+v ok=%v", found, ok)
	}
}
