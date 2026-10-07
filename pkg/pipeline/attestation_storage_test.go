package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

const legacyAttestationTestJSON = `{"_type":"https://in-toto.io/Statement/v0.1","subject":[],"predicateType":"https://ai-team.dev/attestation/run/v1","predicate":{"schema_version":1,"run_id":"legacy-run","outcome":"passed"}}`

func TestAttestationRecordForRunFallbackPriorityAndCorruption(t *testing.T) {
	target := t.TempDir()
	runDir := filepath.Join(target, ".ai-team", "runs", "legacy-run")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(legacyAttestationTestJSON)
	legacyPath := filepath.Join(runDir, "attestation.json")
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := attestationRecordForRun(runDir)
	if err != nil || string(got) != string(legacy) {
		t.Fatalf("legacy fallback: %s %v", got, err)
	}
	controller := []byte(strings.Replace(legacyAttestationTestJSON, `"passed"`, `"failed"`, 1))
	if err := (attest.ControllerStore{TargetDir: target}).Write("legacy-run", controller); err != nil {
		t.Fatal(err)
	}
	got, err = attestationRecordForRun(runDir)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := attest.Parse(got)
	if err != nil || statement.Predicate.Outcome != "failed" {
		t.Fatalf("controller did not win: %+v %v", statement, err)
	}
	controllerPath := filepath.Join(target, ".ai-team", "state", "attestation", "legacy-run.json")
	if err := os.WriteFile(controllerPath, []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := attestationRecordForRun(runDir); err == nil {
		t.Fatal("corrupt controller record fell back to legacy file")
	}
}

type captureAttestationWriter func(*attest.Statement) error

func (f captureAttestationWriter) WriteAttestation(s *attest.Statement) error { return f(s) }

func TestBubblewrapAttestationWriterCachesDigestForDeferredDelivery(t *testing.T) {
	target := t.TempDir()
	runID := "cached-attestation-run"
	started := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	runsRoot := filepath.Join(target, ".ai-team", "runs")
	if err := os.MkdirAll(runsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.Start(runsRoot, evidence.RunManifest{
		RunID: runID, Feature: "feat", TargetDir: target, StartedAt: started,
		ConfigSnapshot: json.RawMessage(`{"profile":"test"}`), WorkflowSnapshot: json.RawMessage(`{"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	var submitted *attest.Statement
	p := &Pipeline{attestationWriter: captureAttestationWriter(func(s *attest.Statement) error { submitted = s; return nil })}
	rs := &runState{p: p, evidence: evidenceStore, approvalStore: approvals, runID: runID, runCfg: RunConfig{TargetDir: target}}
	if err := rs.writeAttestation(started.Add(time.Minute), "completed"); err != nil {
		t.Fatalf("controller API writer mode: %v", err)
	}
	if submitted == nil {
		t.Fatal("typed attestation writer was not called")
	}
	want, err := attest.Digest(submitted)
	if err != nil {
		t.Fatal(err)
	}
	if rs.attestationDigest != want {
		t.Fatalf("cached digest %q != submitted statement digest %q", rs.attestationDigest, want)
	}
	// No local attestation exists and the controller copy is outside this read
	// path. Deferred delivery must use the in-memory digest from finalize.
	if _, err := os.Stat(filepath.Join(evidenceStore.RunDir(), "attestation.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected child-readable attestation file: %v", err)
	}
	got, err := rs.deferredAttestationDigest()
	if err != nil || got != want {
		t.Fatalf("deferred delivery digest=%q err=%v, want %q", got, err, want)
	}
}
