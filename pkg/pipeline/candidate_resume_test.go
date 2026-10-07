package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
)

func TestLoadResumeCandidateFailsClosedWhenControllerMetadataMissing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidate  provenance.Digest
		provenance bool
	}{{
		name:       "candidate-backed",
		candidate:  provenance.Digest{Type: "sha256", Value: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		provenance: true,
	}, {
		name:      "candidate-less according to worker-writable run manifest",
		candidate: provenance.UnknownDigest(), provenance: true,
	}, {
		name: "ambiguous run without provenance",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			runID := "resume-candidate-run"
			runDir := filepath.Join(target, ".ai-team", "runs", runID)
			if err := os.MkdirAll(runDir, 0755); err != nil {
				t.Fatal(err)
			}
			manifest := evidence.RunManifest{SchemaVersion: evidence.SchemaVersion, RunID: runID, TargetDir: target}
			if tc.provenance {
				p := provenance.New(runID, time.Now().UTC())
				p.Add(provenance.KindCandidate, "", tc.candidate)
				data, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Provenance = data
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "run.json"), data, 0644); err != nil {
				t.Fatal(err)
			}

			manager, err := loadResumeCandidate(context.Background(), target, runID, missingCandidateMetadataStore{})
			if err == nil || manager != nil {
				t.Fatalf("missing controller metadata must fail closed regardless of run.json: manager=%v err=%v", manager, err)
			}
		})
	}
}

func TestLoadResumeCandidatePreservesLocalNonGitCompatibility(t *testing.T) {
	target := t.TempDir()
	runID := "resume-local-nongit"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := provenance.New(runID, time.Now().UTC())
	p.Add(provenance.KindCandidate, "", provenance.UnknownDigest())
	provenanceJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(evidence.RunManifest{SchemaVersion: evidence.SchemaVersion, RunID: runID, TargetDir: target, Provenance: provenanceJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := loadResumeCandidate(context.Background(), target, runID, nil)
	if err != nil || manager != nil {
		t.Fatalf("local CLI legacy non-Git resume should remain compatible: manager=%v err=%v", manager, err)
	}
}

func TestTamperedRunManifestCannotBypassMissingCandidateMetadata(t *testing.T) {
	target := t.TempDir()
	runID := "resume-candidate-run"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := evidence.RunManifest{SchemaVersion: evidence.SchemaVersion, RunID: runID, TargetDir: target}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), encoded, 0644); err != nil {
		t.Fatal(err)
	}

	// The worker can write this file and claim candidate=sha256/unknown. That
	// claim must never authorize skipping a missing controller-owned record.
	p := provenance.New(runID, time.Now().UTC())
	p.Add(provenance.KindCandidate, "", provenance.UnknownDigest())
	provenanceJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Provenance = provenanceJSON
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), tampered, 0644); err != nil {
		t.Fatal(err)
	}

	manager, err := loadResumeCandidate(context.Background(), target, runID, missingCandidateMetadataStore{})
	if err == nil || manager != nil {
		t.Fatalf("tampered run.json bypassed missing candidate metadata: manager=%v err=%v", manager, err)
	}
}

type missingCandidateMetadataStore struct{}

func (missingCandidateMetadataStore) Create(candidate.Metadata) error { return os.ErrNotExist }
func (missingCandidateMetadataStore) Read(string, string) (candidate.Metadata, error) {
	return candidate.Metadata{}, os.ErrNotExist
}

func TestLoadResumeCandidateDoesNotTreatMetadataReadFailureAsAbsence(t *testing.T) {
	target := t.TempDir()
	runID := "resume-candidate-run"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := provenance.New(runID, time.Now().UTC())
	p.Add(provenance.KindCandidate, "", provenance.UnknownDigest())
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(evidence.RunManifest{SchemaVersion: evidence.SchemaVersion, RunID: runID, TargetDir: target, Provenance: encoded})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	store := failingCandidateMetadataStore{err: errors.New("controller unavailable")}
	if _, err := loadResumeCandidate(context.Background(), target, runID, store); err == nil || !errors.Is(err, store.err) {
		t.Fatalf("controller failure must not be treated as candidate absence: %v", err)
	}
}

type failingCandidateMetadataStore struct{ err error }

func (s failingCandidateMetadataStore) Create(candidate.Metadata) error { return s.err }
func (s failingCandidateMetadataStore) Read(string, string) (candidate.Metadata, error) {
	return candidate.Metadata{}, s.err
}
