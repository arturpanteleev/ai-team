package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// ListControllerStageDeliveryReceipts returns the validated stage delivery
// receipts the controller wrote for a run, oldest first. A run whose delivery
// stage happened before code review has these and no terminal receipt, so a
// display of "was the branch delivered" must read both. A missing directory
// means none; a receipt that does not validate or belongs to another run is an
// error rather than being skipped.
func ListControllerStageDeliveryReceipts(targetDir, runID string) ([]StageDeliveryReceipt, error) {
	if err := validateTerminalRunID(runID); err != nil {
		return nil, err
	}
	dir := filepath.Join(targetDir, ".ai-team", "state", "stage-delivery-receipts", runID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []StageDeliveryReceipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := safeio.ReadRegularFile(filepath.Join(dir, entry.Name()), 1<<20)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var receipt StageDeliveryReceipt
		if err := decoder.Decode(&receipt); err != nil {
			return nil, fmt.Errorf("stage delivery receipt %s decode: %w", entry.Name(), err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("stage delivery receipt %s: trailing data", entry.Name())
		}
		if err := receipt.Validate(); err != nil {
			return nil, err
		}
		if receipt.RunID != runID {
			return nil, fmt.Errorf("stage delivery receipt %s belongs to run %s", entry.Name(), receipt.RunID)
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].PerformedAt.Before(receipts[j].PerformedAt) })
	return receipts, nil
}
