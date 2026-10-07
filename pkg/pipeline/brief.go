package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const maxBriefBytes = 256 << 10

type briefVersion struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	ParentID   string `json:"parent_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Kind       string `json:"kind"`
}

// BriefVersion is the immutable, run-scoped identity of a business brief.
// Path is always relative to the controller's run store when returned by a
// BriefStore. Pipeline materializes the bytes privately before passing an
// artifact path to a runtime.
type BriefVersion = briefVersion

type BriefDocument struct {
	Version BriefVersion `json:"version"`
	Content []byte       `json:"content"`
}

func materializeBriefDocument(workspace string, document BriefDocument) (briefVersion, error) {
	version := document.Version
	name := filepath.Base(filepath.FromSlash(version.Path))
	if name == "." || name == string(filepath.Separator) || name == "" ||
		version.Path != filepath.ToSlash(filepath.Join("brief", name)) || !strings.HasSuffix(name, ".md") {
		return briefVersion{}, errors.New("controller returned invalid brief version path")
	}
	if len(document.Content) == 0 || len(document.Content) > maxBriefBytes {
		return briefVersion{}, errors.New("controller returned invalid brief content size")
	}
	root := filepath.Join(workspace, "brief")
	path := filepath.Join(root, name)
	if err := writeImmutable(path, document.Content); err != nil {
		return briefVersion{}, err
	}
	local, err := makeBriefVersion(path, version.Kind, version.ParentID, version.ApprovalID)
	if err != nil {
		return briefVersion{}, err
	}
	if local.ID != version.ID || local.SHA256 != version.SHA256 {
		return briefVersion{}, errors.New("controller business brief identity does not match its content")
	}
	local.Path = path
	return local, nil
}

// BriefStore is the typed persistence boundary for durable business briefs.
// It deliberately accepts no filesystem paths from callers.
type BriefStore interface {
	CreateInitial(runID, intention string) (BriefDocument, error)
	AppendClarification(runID, approvalID, questions, answer string) (BriefDocument, error)
	List(runID string) ([]BriefVersion, error)
	Read(runID, versionID string) (BriefDocument, error)
}

type FileBriefStore struct{ targetDir string }

func NewFileBriefStore(targetDir string) *FileBriefStore {
	return &FileBriefStore{targetDir: targetDir}
}

func (s *FileBriefStore) runRoot(runID string) (string, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return "", fmt.Errorf("invalid business brief run id: %w", err)
	}
	root, err := safeio.EnsureDir(s.targetDir, ".ai-team", "runs", runID, "brief")
	if err != nil {
		return "", fmt.Errorf("prepare business brief directory: %w", err)
	}
	return root, nil
}

func (s *FileBriefStore) CreateInitial(runID, intention string) (BriefDocument, error) {
	intention = strings.TrimSpace(intention)
	if intention == "" || len(intention) > maxBriefBytes {
		return BriefDocument{}, errors.New("business intention must contain 1..262144 bytes")
	}
	root, err := s.runRoot(runID)
	if err != nil {
		return BriefDocument{}, err
	}
	content := []byte("# Исходное намерение\n\n" + intention + "\n")
	path := filepath.Join(root, "0001-intention.md")
	if err := writeImmutable(path, content); err != nil {
		return BriefDocument{}, err
	}
	version, err := makeBriefVersion(path, "intention", "", "")
	if err != nil {
		return BriefDocument{}, err
	}
	version.Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(path)))
	return BriefDocument{Version: version, Content: content}, nil
}

func (s *FileBriefStore) AppendClarification(runID, approvalID, questions, answer string) (BriefDocument, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return BriefDocument{}, errors.New("answer must contain 1..16384 bytes")
	}
	root, err := s.runRoot(runID)
	if err != nil {
		return BriefDocument{}, err
	}
	versions, err := listBriefVersions(root)
	if err != nil || len(versions) == 0 {
		return BriefDocument{}, errors.Join(errors.New("versioned business brief missing"), err)
	}
	for _, existing := range versions {
		if strings.Contains(filepath.Base(existing.Path), "-answer-"+filepath.Base(approvalID)+".") {
			data, readErr := safeio.ReadRegularFile(filepath.Join(root, filepath.Base(existing.Path)), maxBriefBytes)
			if readErr != nil {
				return BriefDocument{}, readErr
			}
			existing.Kind, existing.ApprovalID = "clarification", approvalID
			existing.Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(existing.Path)))
			return BriefDocument{Version: existing, Content: data}, nil
		}
	}
	parent := versions[len(versions)-1]
	parentData, err := safeio.ReadRegularFile(parent.Path, maxBriefBytes)
	if err != nil {
		return BriefDocument{}, err
	}
	name := fmt.Sprintf("%04d-answer-%s.md", len(versions)+1, filepath.Base(approvalID))
	path := filepath.Join(root, name)
	addition := []byte(fmt.Sprintf("\n## Уточнение %d\n\n### Вопросы аналитика\n\n%s\n\n### Ответ Product Owner\n\n%s\n", len(versions), strings.TrimSpace(questions), answer))
	content := append(append([]byte(nil), parentData...), addition...)
	if len(content) > maxBriefBytes {
		return BriefDocument{}, errors.New("versioned business brief exceeds 262144 bytes")
	}
	if err := writeImmutable(path, content); err != nil {
		return BriefDocument{}, err
	}
	version, err := makeBriefVersion(path, "clarification", parent.ID, approvalID)
	if err != nil {
		return BriefDocument{}, err
	}
	version.Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(path)))
	return BriefDocument{Version: version, Content: content}, nil
}

func (s *FileBriefStore) List(runID string) ([]BriefVersion, error) {
	root, err := s.runRoot(runID)
	if err != nil {
		return nil, err
	}
	versions, err := listBriefVersions(root)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		versions[i].Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(versions[i].Path)))
	}
	return versions, nil
}

func (s *FileBriefStore) Read(runID, versionID string) (BriefDocument, error) {
	versions, err := s.List(runID)
	if err != nil {
		return BriefDocument{}, err
	}
	for _, version := range versions {
		if version.ID != versionID {
			continue
		}
		root, _ := s.runRoot(runID)
		content, readErr := safeio.ReadRegularFile(filepath.Join(root, filepath.Base(version.Path)), maxBriefBytes)
		if readErr != nil {
			return BriefDocument{}, readErr
		}
		return BriefDocument{Version: version, Content: content}, nil
	}
	return BriefDocument{}, os.ErrNotExist
}

type approvedSpecPayload struct {
	Kind             string            `json:"kind"`
	BriefVersion     briefVersion      `json:"brief_version"`
	AnalystAttemptID string            `json:"analyst_attempt_id"`
	Artifacts        map[string]string `json:"artifacts"`
}

func initialBriefPath(targetDir, runID string) string {
	return filepath.Join(targetDir, ".ai-team", "runs", runID, "brief", "0001-intention.md")
}

func writeInitialBrief(targetDir, runID, intention string) (briefVersion, runtime.Artifact, error) {
	intention = strings.TrimSpace(intention)
	if intention == "" || len(intention) > maxBriefBytes {
		return briefVersion{}, runtime.Artifact{}, errors.New("business intention must contain 1..262144 bytes")
	}
	content := []byte("# Исходное намерение\n\n" + intention + "\n")
	path := initialBriefPath(targetDir, runID)
	if err := writeImmutable(path, content); err != nil {
		return briefVersion{}, runtime.Artifact{}, err
	}
	version, err := makeBriefVersion(path, "intention", "", "")
	if err != nil {
		return briefVersion{}, runtime.Artifact{}, err
	}
	return version, briefArtifact(version), nil
}

// appendClarificationVersion records each human answer as a new immutable brief
// snapshot. Deterministic approval filenames make retries idempotent after a
// process restart.
func appendClarificationVersion(targetDir, runID, approvalID, questions, answer string) (briefVersion, runtime.Artifact, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return briefVersion{}, runtime.Artifact{}, errors.New("answer must contain 1..16384 bytes")
	}
	root := filepath.Join(targetDir, ".ai-team", "runs", runID, "brief")
	versions, err := listBriefVersions(root)
	if err != nil || len(versions) == 0 {
		return briefVersion{}, runtime.Artifact{}, errors.Join(errors.New("versioned business brief missing"), err)
	}
	for _, existing := range versions {
		if strings.Contains(filepath.Base(existing.Path), "-answer-"+filepath.Base(approvalID)+".") {
			existing.Kind = "clarification"
			existing.ApprovalID = approvalID
			return existing, briefArtifact(existing), nil
		}
	}
	parent := versions[len(versions)-1]
	parentData, err := safeio.ReadRegularFile(parent.Path, maxBriefBytes)
	if err != nil {
		return briefVersion{}, runtime.Artifact{}, err
	}
	name := fmt.Sprintf("%04d-answer-%s.md", len(versions)+1, filepath.Base(approvalID))
	path := filepath.Join(root, name)
	addition := []byte(fmt.Sprintf(
		"\n## Уточнение %d\n\n### Вопросы аналитика\n\n%s\n\n### Ответ Product Owner\n\n%s\n",
		len(versions), strings.TrimSpace(questions), answer,
	))
	content := append(append([]byte(nil), parentData...), addition...)
	if len(content) > maxBriefBytes {
		return briefVersion{}, runtime.Artifact{}, errors.New("versioned business brief exceeds 262144 bytes")
	}
	if err := writeImmutable(path, content); err != nil {
		return briefVersion{}, runtime.Artifact{}, err
	}
	version, err := makeBriefVersion(path, "clarification", parent.ID, approvalID)
	if err != nil {
		return briefVersion{}, runtime.Artifact{}, err
	}
	return version, briefArtifact(version), nil
}

func makeBriefVersion(path, kind, parentID, approvalID string) (briefVersion, error) {
	data, err := safeio.ReadRegularFile(path, maxBriefBytes)
	if err != nil {
		return briefVersion{}, err
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	version := briefVersion{ID: "brief-" + hash[:16], Path: path, SHA256: hash,
		ParentID: parentID, ApprovalID: approvalID, Kind: kind}
	metadata := struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}{ID: version.ID, Path: filepath.ToSlash(filepath.Join("brief", filepath.Base(path))), SHA256: hash}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return briefVersion{}, err
	}
	if err := writeImmutable(strings.TrimSuffix(path, filepath.Ext(path))+".json", append(encoded, '\n')); err != nil {
		return briefVersion{}, err
	}
	return version, nil
}

func listBriefVersions(root string) ([]briefVersion, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(paths)
	versions := make([]briefVersion, 0, len(paths))
	for _, path := range paths {
		version, err := makeBriefVersion(path, "", "", "")
		if err != nil {
			return nil, err
		}
		if len(versions) == 0 {
			version.Kind = "intention"
		} else {
			version.Kind = "clarification"
			version.ParentID = versions[len(versions)-1].ID
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if marker := strings.Index(name, "-answer-"); marker >= 0 {
				version.ApprovalID = name[marker+len("-answer-"):]
			}
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func writeImmutable(path string, data []byte) error {
	if err := safeio.WriteRegularFileNoFollow(path, data, 0o444); err == nil {
		return nil
	} else {
		if info, statErr := os.Lstat(path); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return err
		}
		existing, readErr := safeio.ReadRegularFile(path, maxBriefBytes)
		if readErr != nil || string(existing) != string(data) {
			return fmt.Errorf("existing immutable brief version differs: %w", err)
		}
	}
	return nil
}

func briefArtifact(version briefVersion) runtime.Artifact {
	info, _ := os.Stat(version.Path)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return runtime.Artifact{Name: "business-brief", Path: version.Path, Size: size}
}

func briefVersionArtifact(version briefVersion) runtime.Artifact {
	path := strings.TrimSuffix(version.Path, filepath.Ext(version.Path)) + ".json"
	info, _ := os.Stat(path)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return runtime.Artifact{Name: "business-brief-version", Path: path, Size: size}
}

func briefInputs(version briefVersion) []runtime.Artifact {
	return []runtime.Artifact{briefArtifact(version), briefVersionArtifact(version)}
}

func encodeApprovedSpec(version briefVersion, attemptID string, outputs []runtime.Artifact) (json.RawMessage, error) {
	artifacts := make(map[string]string, 2)
	for _, output := range outputs {
		if output.Name != "proposal" && output.Name != "spec" {
			continue
		}
		_, _, digest, err := evidenceDigest(output.Path)
		if err != nil {
			return nil, fmt.Errorf("digest approved %s: %w", output.Name, err)
		}
		artifacts[output.Name] = digest
	}
	if artifacts["proposal"] == "" && artifacts["spec"] == "" {
		// Compatibility for small/custom workflows that do not declare a
		// product-spec contract. The bundled analyst declares both artifacts.
		return nil, nil
	}
	if artifacts["proposal"] == "" || artifacts["spec"] == "" {
		// Workflows without the bundled two-artifact analyst contract retain
		// their custom approval payloads.
		return nil, nil
	}
	version.Path = filepath.ToSlash(filepath.Join("brief", filepath.Base(version.Path)))
	encoded, err := json.Marshal(approvedSpecPayload{Kind: "agreed_spec", BriefVersion: version,
		AnalystAttemptID: attemptID, Artifacts: artifacts})
	return encoded, err
}

func evidenceDigest(path string) (string, int64, string, error) {
	return evidence.ArtifactDigest(path)
}
