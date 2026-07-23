package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

// DeliveredRun описывает прошлый run, доставивший фичу до конца (deployer
// зафиксировал commit и/или создал PR).
type DeliveredRun struct {
	RunID     string
	StartedAt time.Time
	Delivery  delivery.Result
}

// FindDelivered ищет среди прошлых run'ов в runsRoot самый недавний, который
// довёл feature до успешной delivery. Повреждённые или незавершённые run
// пропускаются молча — это best-effort diagnostic сигнал для CLI, а не
// источник истины для delivery-решений контроллера.
func FindDelivered(runsRoot, feature string) (result DeliveredRun, ok bool, err error) {
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return DeliveredRun{}, false, nil
		}
		return DeliveredRun{}, false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(runsRoot, entry.Name())
		runData, readErr := os.ReadFile(filepath.Join(runDir, "run.json"))
		if readErr != nil {
			continue
		}
		var manifest RunManifest
		if jsonErr := json.Unmarshal(runData, &manifest); jsonErr != nil {
			continue
		}
		if manifest.Feature != feature {
			continue
		}

		attemptEntries, readErr := os.ReadDir(filepath.Join(runDir, "attempts"))
		if readErr != nil {
			continue
		}
		for _, attemptEntry := range attemptEntries {
			if !attemptEntry.IsDir() {
				continue
			}
			attemptData, readErr := os.ReadFile(filepath.Join(runDir, "attempts", attemptEntry.Name(), "manifest.json"))
			if readErr != nil {
				continue
			}
			var attempt AttemptManifest
			if jsonErr := json.Unmarshal(attemptData, &attempt); jsonErr != nil {
				continue
			}
			if attempt.Stage != "deployer" || attempt.Delivery == nil {
				continue
			}
			if attempt.Delivery.CommitSHA == "" && attempt.Delivery.PRURL == "" {
				continue
			}
			if !ok || manifest.StartedAt.After(result.StartedAt) {
				result = DeliveredRun{RunID: manifest.RunID, StartedAt: manifest.StartedAt, Delivery: *attempt.Delivery}
				ok = true
			}
		}
	}
	return result, ok, nil
}
