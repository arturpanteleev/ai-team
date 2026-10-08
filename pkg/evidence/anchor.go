package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
const AnchorSchemaVersion = 2

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
	SchemaVersion      int       `json:"schema_version"`
	RunID              string    `json:"run_id"`
	TerminalEvent      string    `json:"terminal_event"`
	EventCount         uint64    `json:"event_count"`
	ChainRootSHA256    string    `json:"chain_root_sha256"`
	ManifestsDigest    string    `json:"manifests_digest"`
	RunManifestSHA256  string    `json:"run_manifest_sha256"`
	SupplementalSHA256 string    `json:"supplemental_sha256"`
	CreatedAt          time.Time `json:"created_at"`
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
	manifestData, err := safeio.ReadRegularFile(filepath.Join(s.RunDir(), "run.json"), 1<<20)
	if err != nil {
		return fmt.Errorf("read run.json for anchor: %w", err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("decode run.json for anchor: %w", err)
	}
	if manifest.RunID != s.runID {
		return fmt.Errorf("run.json identity %q does not match anchor run %q", manifest.RunID, s.runID)
	}
	runManifestSHA256 := sha256Bytes(manifestData)
	supplementalSHA256, err := supplementalDigest(s.RunDir(), manifest.TargetDir, s.runID)
	if err != nil {
		return err
	}
	anchor := Anchor{
		SchemaVersion:      AnchorSchemaVersion,
		RunID:              s.runID,
		TerminalEvent:      terminalEvent,
		EventCount:         events[len(events)-1].Sequence,
		ChainRootSHA256:    events[len(events)-1].SHA256,
		ManifestsDigest:    digest,
		RunManifestSHA256:  runManifestSHA256,
		SupplementalSHA256: supplementalSHA256,
		CreatedAt:          time.Now().UTC(),
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
	return VerifyAnchorWithSources(runDir, nil, nil)
}

func VerifyAnchorWithEventSource(runDir string, eventSource EventLog) error {
	return VerifyAnchorWithSources(runDir, eventSource, nil)
}

// VerifyAnchorWithSources verifies a terminal anchor using the explicitly
// selected event and attempt-manifest authorities. Passing both sources is
// required when verifying a portable bundle so a path-shaped bundle cannot
// auto-resolve live controller records from its surrounding workspace.
func VerifyAnchorWithSources(runDir string, eventSource EventLog, manifestSource AttemptManifestSource) error {
	return VerifyAnchorWithSourcesAndTarget(runDir, eventSource, manifestSource, "")
}

// VerifyAnchorWithSourcesAndTarget verifies an anchor with explicit evidence
// authorities and an optional manifest-recorded target for portable replay.
// The target is used only for lexical delivery-state identity checks; replay
// never opens a path named by an event.
func VerifyAnchorWithSourcesAndTarget(runDir string, eventSource EventLog, manifestSource AttemptManifestSource, deliveryTargetDir string) error {
	return verifyAnchorWithSourcesAndTarget(runDir, eventSource, manifestSource, deliveryTargetDir, true)
}

// VerifyBundleAnchor verifies the immutable run and event/attempt bindings in
// a portable bundle. Supplemental run files are intentionally omitted from
// bundles, so their source-side digest is checked by Build before export.
func VerifyBundleAnchor(runDir string, eventSource EventLog, manifestSource AttemptManifestSource, deliveryTargetDir string) error {
	return verifyAnchorWithSourcesAndTarget(runDir, eventSource, manifestSource, deliveryTargetDir, false)
}

func verifyAnchorWithSourcesAndTarget(runDir string, eventSource EventLog, manifestSource AttemptManifestSource, deliveryTargetDir string, verifySupplemental bool) error {
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
	manifestDigest := sha256Bytes(manifestData)
	if anchor.RunManifestSHA256 == "" || manifestDigest != anchor.RunManifestSHA256 {
		return fmt.Errorf("anchor verify: run.json tampering обнаружен — run_manifest_sha256 не совпадает")
	}
	if anchor.SupplementalSHA256 == "" {
		return fmt.Errorf("anchor verify: supplemental_sha256 отсутствует")
	}
	if verifySupplemental {
		supplementalDigest, digestErr := supplementalDigest(runDir, manifest.TargetDir, manifest.RunID)
		if digestErr != nil {
			return fmt.Errorf("anchor verify: supplemental evidence: %w", digestErr)
		}
		if supplementalDigest != anchor.SupplementalSHA256 {
			return fmt.Errorf("anchor verify: supplemental evidence tampering обнаружен — supplemental_sha256 не совпадает")
		}
	}
	if anchor.RunID != manifest.RunID {
		return fmt.Errorf("anchor verify: anchor run_id %q не совпадает с manifest %q — evidence подменён", anchor.RunID, manifest.RunID)
	}
	if !isTerminalEventType(anchor.TerminalEvent) {
		return fmt.Errorf("anchor verify: недопустимый terminal_event %q", anchor.TerminalEvent)
	}
	replayed, err := ReplayEventLogWithEventSourcesAndTarget(filepath.Join(runDir, "events.jsonl"), manifest.RunID, eventSource, manifestSource, deliveryTargetDir)
	if err != nil {
		return fmt.Errorf("anchor verify: event chain сломан: %w", err)
	}
	events, err := VerifyEventLogWithSource(filepath.Join(runDir, "events.jsonl"), manifest.RunID, eventSource)
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

// supplementalDigest binds all ordinary files in a run directory outside the
// event, manifest, attempt and delivery authorities. In particular, logs,
// reports, candidate.json, usage.json and containment.json are included.
// Attempt files are verified against their manifests separately; attestation
// is validated separately and delivery.json is post-terminal with its own
// validated record digest. Controller-owned usage and containment receipts
// are included under stable synthetic names.
func supplementalDigest(runDir, targetDir, runID string) (string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(runDir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(runDir, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		first, _, _ := strings.Cut(rel, "/")
		if first == "attempts" || first == "inflight-inputs" || rel == "events.jsonl" || rel == "run.json" || rel == "anchor.json" || rel == "delivery.json" || rel == "attestation.json" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("supplemental evidence %s has unsupported type", rel)
		}
		_, size, digest, err := ArtifactDigest(current)
		if err != nil {
			return fmt.Errorf("digest supplemental evidence %s: %w", rel, err)
		}
		files[rel] = fmt.Sprintf("%d:%s", size, digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	if targetDir != "" {
		for _, name := range []string{"containment", "usage"} {
			path := filepath.Join(targetDir, ".ai-team", "state", name, runID+".json")
			data, readErr := safeio.ReadRegularFile(path, 1<<20)
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			if readErr != nil {
				return "", fmt.Errorf("read controller %s evidence: %w", name, readErr)
			}
			files["@controller/"+name+".json"] = fmt.Sprintf("%d:%s", len(data), sha256Bytes(data))
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", path, files[path])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// SealTerminalEvidence refreshes the terminal anchor after post-terminal
// controller evidence such as usage, containment and attestation is published.
// It preserves the event-chain root and attempt manifest binding.
func (s *Store) SealTerminalEvidence() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventLog == nil {
		return errors.New("event log is unavailable")
	}
	events, err := s.eventLog.Read(s.runID)
	if err != nil {
		return fmt.Errorf("terminal evidence read: %w", err)
	}
	if len(events) == 0 || !isTerminalEventType(events[len(events)-1].Type) {
		return errors.New("terminal evidence seal requires a terminal event")
	}
	return s.writeAnchor(events[len(events)-1].Type, events)
}
