// Package humanartifact stores append-only human revisions and comments for
// run artifacts. Agent-produced artifacts and evidence are never overwritten.
package humanartifact

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
	"sync"
	"syscall"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const MaxContentBytes = 10 << 20
const MaxCommentBytes = 16 << 10

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
	ActorID       string    `json:"actor_id"`
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
func (s *Store) Append(runID, artifactPath, baseRevision, baseSHA, content, comment, actorID string) (Revision, error) {
	runID, artifactPath, actorID = strings.TrimSpace(runID), cleanArtifactPath(artifactPath), strings.TrimSpace(actorID)
	content, comment = strings.ReplaceAll(content, "\r\n", "\n"), strings.TrimSpace(comment)
	if !safeName(runID) || artifactPath == "" || actorID == "" {
		return Revision{}, errors.New("run, artifact path, and actor are required")
	}
	if content == "" && comment == "" {
		return Revision{}, errors.New("revision must contain an edit or comment")
	}
	if len(content) > MaxContentBytes || len(comment) > MaxCommentBytes {
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
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Revision{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	previous, err := latest(dir)
	if err != nil {
		return Revision{}, err
	}
	latestID := ""
	latestSHA := baseSHA
	number := 1
	if previous != nil {
		latestID, latestSHA, number = previous.ID, previous.SHA256, previous.Revision+1
	}
	if baseRevision != latestID {
		return Revision{}, fmt.Errorf("revision conflict: latest is %q", latestID)
	}
	if baseRevision != "" && baseSHA != "" && baseSHA != latestSHA {
		return Revision{}, errors.New("base content hash does not match latest revision")
	}
	if content == "" && previous != nil {
		content = previous.Content
	}
	if content == "" {
		return Revision{}, errors.New("first revision requires edited content")
	}
	value := Revision{SchemaVersion: 1, RunID: runID, ArtifactPath: artifactPath, Revision: number,
		BaseRevision: baseRevision, BaseSHA256: latestSHA, Content: content, Comment: comment,
		ActorID: actorID, CreatedAt: time.Now().UTC(), SHA256: digest([]byte(content))}
	value.ID = fmt.Sprintf("rev-%06d-%s", number, value.SHA256[:16])
	data, err := json.Marshal(value)
	if err != nil {
		return Revision{}, err
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
		data, readErr := safeio.ReadRegularFile(filepath.Join(dir, entry.Name()), MaxContentBytes+MaxCommentBytes+(1<<20))
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
		data, err := safeio.ReadRegularFile(filepath.Join(dir, entries[i].Name()), MaxContentBytes+MaxCommentBytes+(1<<20))
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
