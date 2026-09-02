package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// ExportSchema — версия контракта «проверенного экспорта» (state/exports
// записи), создаваемого и проверяемого V0-4.
const ExportSchema = 1

// ExportRecord — отметка в state/exports/<runID>.json о том, что immutable
// evidence run сформирована в проверенный portable bundle (V0-4). Только run,
// присутствующие в state/exports с verified=true, могут быть pruned; запись
// создаётся контроллером после того, как bundle прошёл полную проверку.
//
// V0-0: guard fail-closed — пока не существует корректной verified-записи,
// --prune-runs не имеет права удалять evidence даже при включённом флаге.
type ExportRecord struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Verified      bool      `json:"verified"`
	Bundle        string    `json:"bundle,omitempty"`
	BundleSHA256  string    `json:"bundle_sha256"`
	ExportedAt    time.Time `json:"exported_at"`
}

// loadVerifiedExports читает state/exports и возвращает множество runID,
// для которых существует полная verified-запись. Fail-closed: damaged,
// нерасшифрованный, не-verified или non-regular (symlink) файл НЕ засчитывается.
func (p *planner) loadVerifiedExports() error {
	root := filepath.Join(p.aiTeam, "state", "exports")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "export-запись должна быть regular JSON-файлом без symlink",
			})
			continue
		}
		record, err := readExportRecord(path)
		if err != nil {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: fmt.Sprintf("нечитаемая payload: %v", err),
			})
			continue
		}
		runID := strings.TrimSuffix(name, ".json")
		if record.RunID != runID || record.SchemaVersion != ExportSchema || !record.Verified || record.BundleSHA256 == "" {
			p.plan.Skipped = append(p.plan.Skipped, Skipped{
				Path: path, Reason: "export-запись не verified, не соответствует run_id или bundle_sha256 пуст",
			})
			continue
		}
		p.exports[runID] = true
	}
	return nil
}

func readExportRecord(path string) (ExportRecord, error) {
	var record ExportRecord
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, nil
}
