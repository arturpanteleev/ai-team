// Package evidence stores immutable run and stage-attempt evidence.
package evidence

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// SchemaVersion 6 binds each run to the exact resolved workflow, config and
// controller executable/toolchain identity, hash-chains lifecycle events and
// records the typed fields required for deterministic lifecycle replay.
const SchemaVersion = 6

const (
	genesisEventHash = "0000000000000000000000000000000000000000000000000000000000000000"
	maxEventLogSize  = 64 << 20
)

type ControllerIdentity struct {
	ExecutableSHA256 string `json:"executable_sha256"`
	GoVersion        string `json:"go_version"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	ModuleVersion    string `json:"module_version,omitempty"`
	VCSRevision      string `json:"vcs_revision,omitempty"`
	VCSModified      string `json:"vcs_modified,omitempty"`
}

type RunManifest struct {
	SchemaVersion          int                `json:"schema_version"`
	RunID                  string             `json:"run_id"`
	Feature                string             `json:"feature"`
	TargetDir              string             `json:"target_dir"`
	StartedAt              time.Time          `json:"started_at"`
	ConfigEvidence         string             `json:"config_evidence"`
	ConfigSHA256           string             `json:"config_sha256"`
	ResolvedWorkflow       string             `json:"resolved_workflow_evidence"`
	ResolvedWorkflowSHA256 string             `json:"resolved_workflow_sha256"`
	Controller             ControllerIdentity `json:"controller"`
	ConfigSnapshot         json.RawMessage    `json:"-"`
	WorkflowSnapshot       json.RawMessage    `json:"-"`
}

type AttemptManifest struct {
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	AttemptID     string           `json:"attempt_id"`
	Stage         string           `json:"stage"`
	StageIndex    int              `json:"stage_index"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at"`
	Status        string           `json:"status"`
	Execution     string           `json:"execution"`
	Decision      string           `json:"decision"`
	Outcome       string           `json:"outcome"`
	Verdict       string           `json:"verdict,omitempty"`
	Blocker       string           `json:"blocker,omitempty"`
	Error         string           `json:"error,omitempty"`
	Inputs        []ArtifactRecord `json:"inputs,omitempty"`
	Outputs       []ArtifactRecord `json:"outputs,omitempty"`
	Checks        []checks.Result  `json:"checks,omitempty"`
	Mutations     []string         `json:"mutations,omitempty"`
	Delivery      *delivery.Result `json:"delivery,omitempty"`
}

// ArtifactDigest exposes the same bounded evidence identity used by attempt
// manifests for checkpoint subjects and other controller decisions.
func ArtifactDigest(path string) (artifactType string, size int64, digest string, err error) {
	return hashArtifact(path)
}

type Artifact struct {
	Name       string
	Path       string
	SourcePath string
}

type ArtifactRecord struct {
	Name                  string `json:"name"`
	Type                  string `json:"type"`
	SourcePath            string `json:"source_path"`
	EvidencePath          string `json:"evidence_path"`
	ProducerStage         string `json:"producer_stage,omitempty"`
	ProducerRunID         string `json:"producer_run_id,omitempty"`
	ProducerAttemptID     string `json:"producer_attempt_id,omitempty"`
	ConsumedByRunID       string `json:"consumed_by_run_id,omitempty"`
	ConsumedByAttemptID   string `json:"consumed_by_attempt_id,omitempty"`
	ExternalOrLegacyInput bool   `json:"external_or_legacy_input,omitempty"`
	Size                  int64  `json:"size"`
	SHA256                string `json:"sha256"`
}

type Event struct {
	SchemaVersion  int            `json:"schema_version"`
	Sequence       uint64         `json:"sequence"`
	RunID          string         `json:"run_id"`
	Type           string         `json:"type"`
	Stage          string         `json:"stage,omitempty"`
	AttemptID      string         `json:"attempt_id,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
	Data           map[string]any `json:"data,omitempty"`
	PreviousSHA256 string         `json:"previous_sha256"`
	SHA256         string         `json:"sha256"`
}

type Store struct {
	root          string
	runID         string
	mu            sync.Mutex
	nextID        uint64
	lastEventHash string
	provenanceMu  sync.Mutex
	provenance    map[string]ArtifactRecord
}

func NewRunID(now time.Time) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("run id entropy: %w", err)
	}
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

func Start(root string, manifest RunManifest) (*Store, error) {
	if manifest.RunID == "" || manifest.RunID == "." || manifest.RunID == ".." || filepath.Base(manifest.RunID) != manifest.RunID {
		return nil, fmt.Errorf("недопустимый run_id %q", manifest.RunID)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = safeio.EnsureDir(filepath.Dir(root), filepath.Base(root))
	if err != nil {
		return nil, err
	}
	finalDir := filepath.Join(root, manifest.RunID)
	if _, err := os.Stat(finalDir); err == nil {
		return nil, fmt.Errorf("run %s уже существует", manifest.RunID)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(root, ".tmp-"+manifest.RunID+"-")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	if !json.Valid(manifest.ConfigSnapshot) || !json.Valid(manifest.WorkflowSnapshot) {
		return nil, fmt.Errorf("run evidence: config и resolved workflow snapshots обязательны и должны быть JSON")
	}
	manifest.SchemaVersion = SchemaVersion
	manifest.ConfigEvidence = "config.json"
	manifest.ResolvedWorkflow = "workflow.json"
	manifest.ConfigSHA256 = sha256Bytes(manifest.ConfigSnapshot)
	manifest.ResolvedWorkflowSHA256 = sha256Bytes(manifest.WorkflowSnapshot)
	identity, identityErr := currentControllerIdentity()
	if identityErr != nil {
		return nil, identityErr
	}
	manifest.Controller = identity
	if err := writeJSON(filepath.Join(tmpDir, "run.json"), manifest); err != nil {
		return nil, err
	}
	if err := writeImmutableSnapshot(filepath.Join(tmpDir, manifest.ConfigEvidence), manifest.ConfigSnapshot); err != nil {
		return nil, err
	}
	if err := writeImmutableSnapshot(filepath.Join(tmpDir, manifest.ResolvedWorkflow), manifest.WorkflowSnapshot); err != nil {
		return nil, err
	}
	for _, dir := range []string{"attempts", "logs", "reports", "inflight-inputs"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "events.jsonl"), nil, 0644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return nil, err
	}
	cleanup = false
	return &Store{
		root: root, runID: manifest.RunID, lastEventHash: genesisEventHash,
		provenance: make(map[string]ArtifactRecord),
	}, nil
}

func currentControllerIdentity() (ControllerIdentity, error) {
	identity := ControllerIdentity{GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	executable, err := os.Executable()
	if err != nil {
		return identity, fmt.Errorf("controller executable: %w", err)
	}
	_, digest, err := hashFile(executable)
	if err != nil {
		return identity, fmt.Errorf("controller executable hash: %w", err)
	}
	identity.ExecutableSHA256 = digest
	if info, ok := debug.ReadBuildInfo(); ok {
		identity.ModuleVersion = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				identity.VCSRevision = setting.Value
			case "vcs.modified":
				identity.VCSModified = setting.Value
			}
		}
	}
	return identity, nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeImmutableSnapshot(path string, data []byte) error {
	if err := os.WriteFile(path, append([]byte(nil), data...), 0444); err != nil {
		return err
	}
	return nil
}

func (s *Store) RunID() string { return s.runID }

func (s *Store) RunDir() string { return filepath.Join(s.root, s.runID) }

func (s *Store) LogDir() string { return filepath.Join(s.RunDir(), "logs") }

func (s *Store) PublishReportTree(name, source string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("недопустимое имя report tree %q", name)
	}
	reportsDir := filepath.Join(s.RunDir(), "reports")
	finalDir := filepath.Join(reportsDir, name)
	if _, err := os.Stat(finalDir); err == nil {
		return fmt.Errorf("report tree %s уже опубликован", name)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmpDir, err := os.MkdirTemp(reportsDir, ".tmp-"+name+"-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	staged := filepath.Join(tmpDir, name)
	if err := copyArtifact(source, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, finalDir); err != nil {
		return err
	}
	cleanup = false
	return os.Remove(tmpDir)
}

func (s *Store) NewAttemptID(stage string, ordinal int) string {
	return fmt.Sprintf("%s-%03d-%s", s.runID, ordinal, sanitize(stage))
}

// SnapshotInputs copies the exact bytes that will be supplied to the runtime
// into the controller-owned run directory before execution.
func (s *Store) SnapshotInputs(attemptID string, inputs []Artifact) ([]Artifact, func(), error) {
	if attemptID == "" || filepath.Base(attemptID) != attemptID {
		return nil, func() {}, fmt.Errorf("invalid attempt id %q", attemptID)
	}
	root := filepath.Join(s.RunDir(), "inflight-inputs", attemptID)
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	result := make([]Artifact, 0, len(inputs))
	for index, input := range inputs {
		destination := filepath.Join(root, fmt.Sprintf("%03d-%s", index+1, sanitize(input.Name)), filepath.Base(input.Path))
		if err := copyArtifact(input.Path, destination); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("snapshot input %s: %w", input.Name, err)
		}
		result = append(result, Artifact{Name: input.Name, Path: destination, SourcePath: input.Path})
	}
	return result, cleanup, nil
}

// VerifyCheckEvidence confirms that a delivery provenance reference points to
// a controller-published attempt in the named immutable run.
func VerifyCheckEvidence(runsRoot, runID, checkDigest, workspaceDigest string) error {
	attemptsDir := filepath.Join(runsRoot, runID, "attempts")
	entries, err := os.ReadDir(attemptsDir)
	if err != nil {
		return fmt.Errorf("check evidence attempts: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(attemptsDir, entry.Name(), "manifest.json")
		info, statErr := os.Lstat(manifestPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			return readErr
		}
		var manifest AttemptManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.SchemaVersion != SchemaVersion || manifest.RunID != runID {
			continue
		}
		for _, check := range manifest.Checks {
			if checks.VerifyResultDigest(check) && check.EvidenceDigest == checkDigest && check.WorkspaceDigestBefore == workspaceDigest && check.WorkspaceDigestAfter == workspaceDigest &&
				checks.IsTestEvidence(check) {
				return nil
			}
		}
	}
	return fmt.Errorf("check evidence %s для workspace %s не найдена в run %s", checkDigest, workspaceDigest, runID)
}

func (s *Store) PublishAttempt(manifest AttemptManifest, artifactRoot string, inputs, outputs []Artifact) error {
	manifest.SchemaVersion = SchemaVersion
	manifest.RunID = s.runID
	if manifest.AttemptID == "" {
		return fmt.Errorf("attempt_id обязателен")
	}
	attemptsDir := filepath.Join(s.RunDir(), "attempts")
	finalDir := filepath.Join(attemptsDir, manifest.AttemptID)
	if _, err := os.Stat(finalDir); err == nil {
		return fmt.Errorf("attempt %s уже опубликован", manifest.AttemptID)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmpDir, err := os.MkdirTemp(attemptsDir, ".tmp-"+manifest.AttemptID+"-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	for index, input := range inputs {
		sourcePath := input.SourcePath
		if sourcePath == "" {
			sourcePath = input.Path
		}
		if _, relErr := confinedRelative(artifactRoot, sourcePath); relErr != nil {
			return fmt.Errorf("input %s: %w", input.Name, relErr)
		}
		inputName := fmt.Sprintf("%03d-%s", index+1, sanitize(input.Name))
		destination := filepath.Join(tmpDir, "inputs", inputName, filepath.Base(sourcePath))
		if err := copyArtifact(input.Path, destination); err != nil {
			return fmt.Errorf("input %s immutable copy: %w", input.Name, err)
		}
		artifactType, size, digest, hashErr := hashArtifact(destination)
		if hashErr != nil {
			return fmt.Errorf("input %s evidence: %w", input.Name, hashErr)
		}
		evidenceRel := filepath.ToSlash(filepath.Join("attempts", manifest.AttemptID, "inputs", inputName, filepath.Base(sourcePath)))
		record := ArtifactRecord{
			Name: input.Name, Type: artifactType, SourcePath: sourcePath, EvidencePath: evidenceRel,
			ConsumedByRunID: s.runID, ConsumedByAttemptID: manifest.AttemptID, Size: size, SHA256: digest,
		}
		s.provenanceMu.Lock()
		producer, known := s.provenance[cleanArtifactKey(sourcePath)]
		s.provenanceMu.Unlock()
		if known {
			if producer.SHA256 != digest || producer.Type != artifactType {
				return fmt.Errorf("input %s provenance mismatch: live bytes/type no longer match producer %s/%s", input.Name, producer.ProducerRunID, producer.ProducerAttemptID)
			}
			record.ProducerStage = producer.ProducerStage
			record.ProducerRunID = producer.ProducerRunID
			record.ProducerAttemptID = producer.ProducerAttemptID
		} else {
			record.ExternalOrLegacyInput = true
		}
		manifest.Inputs = append(manifest.Inputs, record)
	}
	for _, output := range outputs {
		rel, relErr := confinedRelative(artifactRoot, output.Path)
		if relErr != nil {
			return fmt.Errorf("output %s: %w", output.Name, relErr)
		}
		evidenceRel := filepath.ToSlash(filepath.Join("attempts", manifest.AttemptID, "artifacts", rel))
		destination := filepath.Join(tmpDir, "artifacts", rel)
		if _, statErr := os.Lstat(destination); os.IsNotExist(statErr) {
			if err := copyArtifact(output.Path, destination); err != nil {
				return fmt.Errorf("output %s: %w", output.Name, err)
			}
		} else if statErr != nil {
			return fmt.Errorf("output %s evidence: %w", output.Name, statErr)
		}
		artifactType, size, digest, hashErr := hashArtifact(destination)
		if hashErr != nil {
			return fmt.Errorf("output %s evidence: %w", output.Name, hashErr)
		}
		manifest.Outputs = append(manifest.Outputs, ArtifactRecord{
			Name: output.Name, Type: artifactType, SourcePath: output.Path, EvidencePath: evidenceRel,
			ProducerStage: manifest.Stage, ProducerRunID: s.runID, ProducerAttemptID: manifest.AttemptID,
			Size: size, SHA256: digest,
		})
	}
	if err := writeJSON(filepath.Join(tmpDir, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return err
	}
	cleanup = false
	s.provenanceMu.Lock()
	for _, output := range manifest.Outputs {
		s.provenance[cleanArtifactKey(output.SourcePath)] = output
	}
	s.provenanceMu.Unlock()
	return nil
}

func cleanArtifactKey(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func (s *Store) Append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := VerifyEventLog(filepath.Join(s.RunDir(), "events.jsonl"), s.runID)
	if err != nil {
		return fmt.Errorf("event log integrity: %w", err)
	}
	lastHash := genesisEventHash
	if len(events) > 0 {
		lastHash = events[len(events)-1].SHA256
	}
	if uint64(len(events)) != s.nextID || lastHash != s.lastEventHash {
		return fmt.Errorf("event log changed outside current store")
	}
	event.SchemaVersion = SchemaVersion
	event.Sequence = s.nextID + 1
	event.RunID = s.runID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.PreviousSHA256 = s.lastEventHash
	event.SHA256, err = eventDigest(event)
	if err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.RunDir(), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.nextID = event.Sequence
	s.lastEventHash = event.SHA256
	return nil
}

// VerifyEventLog validates strict JSON records, sequence and the complete hash
// chain. Callers can deterministically detect modification, removal, insertion
// or reordering of any persisted event.
func VerifyEventLog(path, runID string) ([]Event, error) {
	data, err := safeio.ReadRegularFile(path, maxEventLogSize)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	events := make([]Event, 0, len(lines))
	previous := genesisEventHash
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var event Event
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("event %d JSON: %w", len(events)+1, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil {
			return nil, fmt.Errorf("event %d has trailing JSON", len(events)+1)
		} else if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("event %d trailing data: %w", len(events)+1, err)
		}
		if event.SchemaVersion != SchemaVersion || event.Sequence != uint64(len(events)+1) ||
			event.RunID != runID || event.Type == "" || event.Timestamp.IsZero() {
			return nil, fmt.Errorf("event %d identity/sequence is invalid", len(events)+1)
		}
		if event.PreviousSHA256 != previous || !validSHA256(event.SHA256) {
			return nil, fmt.Errorf("event %d chain link is invalid", event.Sequence)
		}
		digest, err := eventDigest(event)
		if err != nil {
			return nil, err
		}
		if digest != event.SHA256 {
			return nil, fmt.Errorf("event %d hash mismatch", event.Sequence)
		}
		events = append(events, event)
		previous = event.SHA256
	}
	return events, nil
}

func eventDigest(event Event) (string, error) {
	event.SHA256 = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	return sha256Bytes(data), nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func confinedRelative(root, value string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	valueAbs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, valueAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь %s находится вне artifact root", value)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	resolvedValue, err := filepath.EvalSymlinks(valueAbs)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedValue)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь %s разрешается вне artifact root", value)
	}
	return rel, nil
}

func hashArtifact(value string) (artifactType string, size int64, digest string, err error) {
	info, err := os.Lstat(value)
	if err != nil {
		return "", 0, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", 0, "", fmt.Errorf("symbolic link %s не разрешён как evidence artifact", value)
	}
	if info.Mode().IsRegular() {
		fileInfo, fileDigest, fileErr := hashFile(value)
		if fileErr != nil {
			return "", 0, "", fileErr
		}
		return "file", fileInfo.Size(), fileDigest, nil
	}
	if !info.IsDir() {
		return "", 0, "", fmt.Errorf("%s имеет неподдерживаемый тип", value)
	}

	h := sha256.New()
	err = filepath.WalkDir(value, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == value {
			return nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %s не разрешён как evidence artifact", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("%s имеет неподдерживаемый тип", path)
		}
		rel, relErr := filepath.Rel(value, path)
		if relErr != nil {
			return relErr
		}
		_, fileDigest, fileErr := hashFile(path)
		if fileErr != nil {
			return fileErr
		}
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", filepath.ToSlash(rel), entryInfo.Size(), fileDigest)
		size += entryInfo.Size()
		return nil
	})
	if err != nil {
		return "", 0, "", err
	}
	return "directory", size, hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(path string) (os.FileInfo, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s не является regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, "", err
	}
	return info, hex.EncodeToString(h.Sum(nil)), nil
}

func copyArtifact(source, destination string) error {
	linkInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic link %s не разрешён как evidence output", source)
	}
	if linkInfo.IsDir() {
		return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(source, path)
			if relErr != nil {
				return relErr
			}
			target := filepath.Join(destination, rel)
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic link %s не разрешён как evidence output", path)
			}
			if entry.IsDir() {
				return os.MkdirAll(target, info.Mode().Perm())
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s имеет неподдерживаемый тип", path)
			}
			return copyRegularFile(path, target)
		})
	}
	return copyRegularFile(source, destination)
}

func copyRegularFile(source, destination string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s не является regular file", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		_ = destinationFile.Close()
		return err
	}
	if err := destinationFile.Sync(); err != nil {
		_ = destinationFile.Close()
		return err
	}
	return destinationFile.Close()
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}

func sanitize(value string) string {
	var result strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			result.WriteRune(r)
		} else {
			result.WriteByte('-')
		}
	}
	clean := strings.Trim(result.String(), "-")
	if clean == "" {
		return "stage"
	}
	return clean
}
