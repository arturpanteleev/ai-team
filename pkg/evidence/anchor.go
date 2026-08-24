package evidence

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
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// AnchorSchemaVersion is the schema of the terminal anchor.json manifest.
const AnchorSchemaVersion = 1

const (
	anchorFileName    = "anchor.json"
	maxAnchorFileSize = 64 << 10
)

// Terminal event types that trigger anchor publication.
func isTerminalEventType(eventType string) bool {
	return eventType == "run_finished" || eventType == "run_canceled"
}

// Anchor — tamper-evident терминальный manifest run: фиксирует длину
// hash-chained event log, корень цепочки и digest всех attempt manifests на
// момент завершения run.
type Anchor struct {
	SchemaVersion   int       `json:"schema_version"`
	RunID           string    `json:"run_id"`
	TerminalEvent   string    `json:"terminal_event"`
	EventCount      uint64    `json:"event_count"`
	ChainRootSHA256 string    `json:"chain_root_sha256"`
	ManifestsDigest string    `json:"manifests_digest"`
	CreatedAt       time.Time `json:"created_at"`
}

// manifestsDigest вычисляет sha256 отсортированного списка
// "attemptID:manifestSHA256" из attempt_finished событий.
func manifestsDigest(events []Event) (string, error) {
	byAttempt := make(map[string]string)
	for _, event := range events {
		if event.Type != "attempt_finished" || !safeEventIdentifier(event.AttemptID) {
			continue
		}
		digest, err := eventString(event.Data, "manifest_sha256", false)
		if err != nil || digest == "" {
			continue
		}
		if !validSHA256(digest) {
			return "", fmt.Errorf("attempt_finished %q manifest digest is invalid", event.AttemptID)
		}
		byAttempt[event.AttemptID] = digest
	}
	lines := make([]string, 0, len(byAttempt))
	for attemptID, digest := range byAttempt {
		lines = append(lines, attemptID+":"+digest)
	}
	sort.Strings(lines)
	return sha256Bytes([]byte(strings.Join(lines, "\n"))), nil
}

// writeAnchor атомарно публикует {RunDir}/anchor.json после terminal события
// (тот же tmp+rename паттерн, что и остальные evidence-артефакты).
func (s *Store) writeAnchor(terminalEvent string, events []Event) error {
	if len(events) == 0 {
		return fmt.Errorf("anchor требует непустой event log")
	}
	digest, err := manifestsDigest(events)
	if err != nil {
		return err
	}
	anchor := Anchor{
		SchemaVersion:   AnchorSchemaVersion,
		RunID:           s.runID,
		TerminalEvent:   terminalEvent,
		EventCount:      events[len(events)-1].Sequence,
		ChainRootSHA256: events[len(events)-1].SHA256,
		ManifestsDigest: digest,
		CreatedAt:       time.Now().UTC(),
	}
	data, err := json.MarshalIndent(anchor, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpFile, err := os.CreateTemp(s.RunDir(), ".tmp-anchor-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(s.RunDir(), anchorFileName))
}

// VerifyAnchor проверяет терминальный anchor manifest run: replay цепочки,
// число событий, корень цепочки и digest attempt manifests. Любое расхождение
// означает tampering или повреждение evidence.
func VerifyAnchor(runDir string) error {
	manifestData, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return fmt.Errorf("anchor verify: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	decoder.DisallowUnknownFields()
	var manifest RunManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("anchor verify: run manifest: %w", err)
	}
	if manifest.RunID == "" {
		return fmt.Errorf("anchor verify: run manifest без run_id")
	}
	anchorData, err := safeio.ReadRegularFile(filepath.Join(runDir, anchorFileName), maxAnchorFileSize)
	if err != nil {
		return fmt.Errorf("anchor verify: run %s не имеет валидного anchor.json: %w", manifest.RunID, err)
	}
	anchorDecoder := json.NewDecoder(bytes.NewReader(anchorData))
	anchorDecoder.DisallowUnknownFields()
	var anchor Anchor
	if err := anchorDecoder.Decode(&anchor); err != nil {
		return fmt.Errorf("anchor verify: anchor.json повреждён: %w", err)
	}
	var trailing any
	if err := anchorDecoder.Decode(&trailing); err == nil {
		return fmt.Errorf("anchor verify: anchor.json содержит trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("anchor verify: anchor.json trailing data: %w", err)
	}
	if anchor.SchemaVersion != AnchorSchemaVersion {
		return fmt.Errorf("anchor verify: неожиданная schema_version %d", anchor.SchemaVersion)
	}
	if anchor.RunID != manifest.RunID {
		return fmt.Errorf("anchor verify: anchor run_id %q не совпадает с manifest %q — evidence подменён", anchor.RunID, manifest.RunID)
	}
	if !isTerminalEventType(anchor.TerminalEvent) {
		return fmt.Errorf("anchor verify: недопустимый terminal_event %q", anchor.TerminalEvent)
	}
	replayed, err := ReplayEventLog(filepath.Join(runDir, "events.jsonl"), manifest.RunID)
	if err != nil {
		return fmt.Errorf("anchor verify: event chain сломан: %w", err)
	}
	events, err := VerifyEventLog(filepath.Join(runDir, "events.jsonl"), manifest.RunID)
	if err != nil {
		return fmt.Errorf("anchor verify: event chain сломан: %w", err)
	}
	if len(events) == 0 {
		return fmt.Errorf("anchor verify: пустой event log")
	}
	if uint64(len(events)) != anchor.EventCount {
		return fmt.Errorf("anchor verify: tampering обнаружен — anchor event_count=%d, фактически %d событий", anchor.EventCount, len(events))
	}
	if replayed.LastEventSHA256 != anchor.ChainRootSHA256 {
		return fmt.Errorf("anchor verify: tampering обнаружен — chain_root_sha256 не совпадает с последним событием (цепочка пересобрана или усечена)")
	}
	digest, err := manifestsDigest(events)
	if err != nil {
		return fmt.Errorf("anchor verify: %w", err)
	}
	if digest != anchor.ManifestsDigest {
		return fmt.Errorf("anchor verify: tampering обнаружен — manifests_digest не совпадает")
	}
	return nil
}
