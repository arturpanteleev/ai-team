package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

// DeliveredRun описывает прошлый run, доставивший фичу до конца
// (delivery-стадия зафиксировала commit и/или создала PR).
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

		// V0-9: канонический источник результата доставки — terminal
		// delivery.json (deferred delivery). Attempt-манифесты stay immutable
		// (CommitSHA пуст), поэтому сначала ищем record, и только при его
		// отсутствии сканируем старые attempt-манифесты.
		record, recOK, recErr := delivery.ReadTerminalRecord(runDir)
		if recErr != nil {
			// QS-23: ошибка чтения канонического record (повреждение или
			// правка руками) раньше просто отбрасывалась, и результат
			// доставки брался из более слабого источника — attempt-манифестов.
			// Run со сломанным delivery.json пропускается целиком: это
			// best-effort диагностика, и молча подставлять вместо
			// непрошедшего проверку record что-то другое нельзя.
			continue
		}
		if recOK && record.Feature == feature {
			if !ok || manifest.StartedAt.After(result.StartedAt) {
				result = DeliveredRun{
					RunID: manifest.RunID, StartedAt: manifest.StartedAt,
					Delivery: delivery.Result{
						PlanHash: record.PlanHash, CommitSHA: record.CommitSHA,
						PRURL: record.PRURL, StatePath: "",
					},
				}
				ok = true
			}
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
			// Delivery-стадию опознаём по наличию delivery-результата, а не
			// по имени стадии: имя задаётся пользователем в конфиге, а вид
			// агента (kind: delivery) в attempt-манифест не попадает, поэтому
			// в evidence-слое его просто нет. Непустой Delivery этого
			// достаточно: поле заполняет только контроллер и только для
			// агента с kind: delivery (pkg/pipeline/stage.go), — ни один
			// другой вид стадии его записать не может.
			if attempt.Delivery == nil {
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
