package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const controllerStatement = `{"_type":"https://in-toto.io/Statement/v0.1","subject":[],"predicateType":"https://ai-team.dev/attestation/run/v1","predicate":{"schema_version":1,"run_id":"store-run","outcome":"passed"}}`

func TestControllerStoreStrictIdempotentConflictAndIdentity(t *testing.T) {
	target := t.TempDir()
	store := ControllerStore{TargetDir: target}
	if err := store.Write("store-run", []byte(controllerStatement)); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("store-run", []byte(controllerStatement)); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if err := store.Write("store-run", []byte(strings.Replace(controllerStatement, `"subject":[]`, `"subject":[],"unknown":true`, 1))); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := store.Write("other-run", []byte(controllerStatement)); err == nil {
		t.Fatal("run mismatch accepted")
	}
	conflict := strings.Replace(controllerStatement, `"passed"`, `"failed"`, 1)
	if err := store.Write("store-run", []byte(conflict)); err == nil {
		t.Fatal("conflicting raw record accepted")
	}
	path := filepath.Join(target, ".ai-team", "state", "attestation", "store-run.json")
	if err := os.WriteFile(path, []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("store-run"); err == nil {
		t.Fatal("corrupt controller record read")
	}
}

func TestControllerStoreRejectsCanonicalRecordOverLimit(t *testing.T) {
	statement := Statement{
		Type:          StatementType,
		PredicateType: PredicateTypeV1,
		Predicate:     Predicate{SchemaVersion: PredicateSchemaVersion, RunID: "store-run", Outcome: "passed"},
		Subject:       make([]Subject, 8000),
	}
	for i := range statement.Subject {
		statement.Subject[i] = Subject{
			Name:   strings.Repeat("n", 20),
			Digest: map[string]string{"sha256": strings.Repeat("a", 64)},
		}
	}
	raw, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > controllerAttestationLimit {
		t.Fatalf("regression input compact size %d exceeds limit %d", len(raw), controllerAttestationLimit)
	}
	canonical, err := json.MarshalIndent(statement, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	canonical = append(canonical, '\n')
	if len(canonical) <= controllerAttestationLimit {
		t.Fatalf("regression input canonical size %d does not exceed limit %d", len(canonical), controllerAttestationLimit)
	}

	target := t.TempDir()
	err = (ControllerStore{TargetDir: target}).Write("store-run", raw)
	if err == nil {
		t.Fatal("record whose canonical form exceeds the read limit was accepted")
	}
	path := filepath.Join(target, ".ai-team", "state", "attestation", "store-run.json")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("oversized canonical record was persisted: stat err = %v", statErr)
	}
}
