// Package humanartifact stores append-only human revisions and comments for
// run artifacts. Agent-produced artifacts and evidence are never overwritten.
package humanartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const MaxContentBytes = 10 << 20
const MaxCommentBytes = 16 << 10
const MaxRevisionRecordBytes = 64 << 20

type Revision struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	ArtifactPath  string    `json:"artifact_path"`
	Revision      int       `json:"revision"`
	ID            string    `json:"id"`
	BaseRevision  string    `json:"base_revision,omitempty"`
	BaseSHA256    string    `json:"base_sha256,omitempty"`
	Content       string    `json:"content,omitempty"`
	Comment       string    `json:"comment,omitempty"`
	Description   string    `json:"description,omitempty"`
	ActorID       string    `json:"actor_id"`
	StageID       string    `json:"stage_id,omitempty"`
	ApprovalID    string    `json:"approval_id,omitempty"`
	Result        string    `json:"result,omitempty"`
	LinkKind      string    `json:"link_kind,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	SHA256        string    `json:"sha256"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func New(targetDir string) (*Store, error) {
	root, err := safeio.EnsureDir(targetDir, ".ai-team", "runs")
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Append applies optimistic concurrency against the latest immutable revision.
// baseRevision must be empty for the first version, otherwise it must equal the
// last revision ID observed by the caller.
func (s *Store) Append(runID, artifactPath, baseRevision, baseSHA, content, comment, actorID string) (result Revision, retErr error) {
	return s.append(runID, artifactPath, baseRevision, baseSHA, content, comment, actorID, submissionMetadata{}, false)
}

// AppendSubmission stores an immutable, stage-scoped human result version.
// ApprovalID makes a retry of the same accepted/pending input idempotent;
// concurrent attempts to submit different bytes for that approval conflict.
func (s *Store) AppendSubmission(runID, stageID, approvalID, resultKind, linkKind, content, description, comment, actorID string) (Revision, error) {
	runID, stageID, approvalID = strings.TrimSpace(runID), strings.TrimSpace(stageID), strings.TrimSpace(approvalID)
	resultKind, linkKind = strings.TrimSpace(resultKind), strings.TrimSpace(linkKind)
	if !safeName(stageID) || !safeName(approvalID) {
		return Revision{}, errors.New("stage and approval are required for a submission")
	}
	if resultKind != "md" && resultKind != "link" && resultKind != "approve" {
		return Revision{}, fmt.Errorf("unsupported human result type %q", resultKind)
	}
	if resultKind == "link" {
		if linkKind != "pr" && linkKind != "build" && linkKind != "other" {
			return Revision{}, fmt.Errorf("unsupported link kind %q", linkKind)
		}
	} else if linkKind != "" {
		return Revision{}, errors.New("link kind is only valid for link results")
	}
	extension := resultKind
	if resultKind == "approve" {
		extension = "txt"
	}
	artifactPath := "stages/" + stageID + "/result." + extension
	metadata := submissionMetadata{StageID: stageID, ApprovalID: approvalID, Result: resultKind, LinkKind: linkKind, Description: description}
	return s.append(runID, artifactPath, "", "", content, comment, actorID, metadata, true)
}

type submissionMetadata struct {
	StageID     string
	ApprovalID  string
	Result      string
	LinkKind    string
	Description string
}

func (s *Store) append(runID, artifactPath, baseRevision, baseSHA, content, comment, actorID string, metadata submissionMetadata, preserveBytes bool) (result Revision, retErr error) {
	runID, artifactPath, actorID = strings.TrimSpace(runID), cleanArtifactPath(artifactPath), strings.TrimSpace(actorID)
	if !preserveBytes {
		content, comment = strings.ReplaceAll(content, "\r\n", "\n"), strings.TrimSpace(comment)
	}
	if !safeName(runID) || artifactPath == "" || actorID == "" {
		return Revision{}, errors.New("run, artifact path, and actor are required")
	}
	if content == "" && comment == "" && metadata.Result != "approve" {
		return Revision{}, errors.New("revision must contain an edit or comment")
	}
	if len(content) > MaxContentBytes || len(comment) > MaxCommentBytes || len(metadata.Description) > MaxCommentBytes {
		return Revision{}, errors.New("revision content exceeds size limit")
	}
	if content != "" && baseSHA != "" && digest([]byte(content)) == baseSHA {
		return Revision{}, errors.New("edit is unchanged from base content")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.directory(runID, artifactPath)
	if err != nil {
		return Revision{}, err
	}
	if err := safeio.EnsureDirPath(dir); err != nil {
		return Revision{}, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Revision{}, err
	}
	locked := false
	defer func() {
		if locked {
			retErr = errors.Join(retErr, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
		}
		retErr = errors.Join(retErr, lock.Close())
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Revision{}, err
	}
	locked = true
	previous, err := latest(dir)
	if err != nil {
		return Revision{}, err
	}
	if metadata.ApprovalID != "" {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return Revision{}, readErr
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			data, readErr := safeio.ReadRegularFile(filepath.Join(dir, entry.Name()), MaxRevisionRecordBytes)
			if readErr != nil {
				return Revision{}, readErr
			}
			var existing Revision
			if err := json.Unmarshal(data, &existing); err != nil {
				return Revision{}, err
			}
			if existing.ApprovalID != metadata.ApprovalID {
				continue
			}
			if existing.Content == content && existing.Comment == comment && existing.Description == metadata.Description && existing.ActorID == actorID &&
				existing.StageID == metadata.StageID && existing.Result == metadata.Result && existing.LinkKind == metadata.LinkKind {
				return existing, nil
			}
			return Revision{}, fmt.Errorf("submission conflict: approval %q already has different bytes", metadata.ApprovalID)
		}
	}
	latestID := ""
	latestSHA := baseSHA
	number := 1
	if previous != nil {
		latestID, latestSHA, number = previous.ID, previous.SHA256, previous.Revision+1
	}
	if metadata.ApprovalID != "" && baseRevision == "" {
		// New human submissions are serialized by the per-stage file lock and
		// automatically build on the latest immutable version. The approval ID
		// still makes retries idempotent and rejects changed bytes.
		baseRevision, baseSHA = latestID, latestSHA
	}
	if baseRevision != latestID {
		return Revision{}, fmt.Errorf("revision conflict: latest is %q", latestID)
	}
	if baseRevision != "" && baseSHA != "" && baseSHA != latestSHA {
		return Revision{}, errors.New("base content hash does not match latest revision")
	}
	if content == "" && previous != nil && metadata.Result == "" {
		content = previous.Content
	}
	if content == "" && metadata.Result != "approve" {
		return Revision{}, errors.New("first revision requires edited content")
	}
	value := Revision{SchemaVersion: 1, RunID: runID, ArtifactPath: artifactPath, Revision: number,
		BaseRevision: baseRevision, BaseSHA256: latestSHA, Content: content, Comment: comment,
		ActorID: actorID, StageID: metadata.StageID, ApprovalID: metadata.ApprovalID,
		Result: metadata.Result, LinkKind: metadata.LinkKind, Description: metadata.Description,
		CreatedAt: time.Now().UTC(), SHA256: digest([]byte(content))}
	value.ID = fmt.Sprintf("rev-%06d-%s", number, value.SHA256[:16])
	data, err := json.Marshal(value)
	if err != nil {
		return Revision{}, err
	}
	if len(data) > MaxRevisionRecordBytes {
		return Revision{}, fmt.Errorf("revision record exceeds maximum size of %d bytes", MaxRevisionRecordBytes)
	}
	path := filepath.Join(dir, fmt.Sprintf("%06d.json", number))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return Revision{}, err
	}
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return Revision{}, err
	}
	return value, nil
}

func (s *Store) List(runID, artifactPath string) ([]Revision, error) {
	if !safeName(runID) {
		return nil, errors.New("invalid run id")
	}
	dir, err := s.directory(runID, cleanArtifactPath(artifactPath))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Revision{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := make([]Revision, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := safeio.ReadRegularFile(filepath.Join(dir, entry.Name()), MaxRevisionRecordBytes)
		if readErr != nil {
			return nil, readErr
		}
		var value Revision
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Revision < values[j].Revision })
	return values, nil
}

// Get returns one immutable revision by ID; it never resolves the moving head.
func (s *Store) Get(runID, artifactPath, revisionID string) (Revision, error) {
	if strings.TrimSpace(revisionID) == "" {
		return Revision{}, errors.New("revision id is required")
	}
	values, err := s.List(runID, artifactPath)
	if err != nil {
		return Revision{}, err
	}
	for _, value := range values {
		if value.ID == revisionID {
			return value, nil
		}
	}
	return Revision{}, fmt.Errorf("human artifact revision %q not found", revisionID)
}

// ValidateSubmission applies the shared typed-result limits used by both the
// CLI/API and pipeline resume. Links are restricted to HTTP(S) URLs and the
// link kind declared by the project's process template.
func ValidateSubmission(resultKind, linkKind, content string) error {
	switch resultKind {
	case "md":
		if linkKind != "" {
			return errors.New("link kind is only valid for link results")
		}
		if len(content) > MaxContentBytes {
			return fmt.Errorf("markdown exceeds maximum size of %d bytes", MaxContentBytes)
		}
		if !utf8.ValidString(content) {
			return errors.New("markdown must be valid UTF-8 text")
		}
		if strings.TrimSpace(content) == "" {
			return errors.New("markdown submission must not be empty")
		}
	case "link":
		if linkKind != "pr" && linkKind != "build" && linkKind != "other" {
			return fmt.Errorf("unsupported link kind %q", linkKind)
		}
		if len(content) > 16<<10 {
			return errors.New("link URL exceeds maximum size of 16384 bytes")
		}
		parsed, err := url.ParseRequestURI(strings.TrimSpace(content))
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return errors.New("link result must be an HTTP(S) URL")
		}
	case "approve":
		if linkKind != "" {
			return errors.New("link kind is only valid for link results")
		}
		if len(content) > MaxCommentBytes {
			return fmt.Errorf("approval text exceeds maximum size of %d bytes", MaxCommentBytes)
		}
	default:
		return fmt.Errorf("unsupported human result type %q", resultKind)
	}
	return nil
}

// Digest returns the lowercase SHA-256 digest of the exact submitted bytes.
func Digest(content []byte) string { return digest(content) }

func (s *Store) directory(runID, artifactPath string) (string, error) {
	if !safeName(runID) || artifactPath == "" {
		return "", errors.New("invalid run or artifact path")
	}
	hash := digest([]byte(artifactPath))
	return filepath.Join(s.root, runID, "human-artifacts", hex.EncodeToString([]byte(hash))[:32]), nil
}

func latest(dir string) (*Revision, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].IsDir() || filepath.Ext(entries[i].Name()) != ".json" {
			continue
		}
		data, err := safeio.ReadRegularFile(filepath.Join(dir, entries[i].Name()), MaxRevisionRecordBytes)
		if err != nil {
			return nil, err
		}
		var value Revision
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		return &value, nil
	}
	return nil, nil
}

func cleanArtifactPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\x00") {
		return ""
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return value
}
func safeName(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}
func digest(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }
