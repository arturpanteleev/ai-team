package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// anchor_manifest_test.go — QS-05: anchor обязан фиксировать digest run.json.
// До фикса содержимое run manifest (feature, target_dir, controller identity,
// provenance digests) не было покрыто ни одной проверкой.

func TestAnchorBindsRunManifest(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-manifest")
	anchor := readAnchor(t, runDir)
	data, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if anchor.RunManifestSHA256 != sha256Bytes(data) {
		t.Fatalf("anchor не фиксирует digest run.json: %q", anchor.RunManifestSHA256)
	}
}

func TestVerifyAnchorDetectsRunManifestTampering(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-tampered-manifest")
	path := filepath.Join(runDir, "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"feature": "`, `"feature": "hijacked-`, 1)
	if tampered == string(data) {
		t.Fatal("подмена не применилась")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	err = VerifyAnchor(runDir)
	if err == nil {
		t.Fatal("подмена run.json должна ломать VerifyAnchor")
	}
	if !strings.Contains(err.Error(), "run_manifest_sha256") {
		t.Fatalf("ожидалась причина про run_manifest_sha256, получено: %v", err)
	}
}

// TestVerifyAnchorRejectsLegacySchema — anchor версии 1 не несёт digest
// run.json, поэтому «проверен полностью» про такой run сказать нельзя:
// fail-closed вместо молчаливого послабления.
func TestVerifyAnchorRejectsLegacySchema(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-legacy")
	path := filepath.Join(runDir, "anchor.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var anchor map[string]any
	if err := json.Unmarshal(data, &anchor); err != nil {
		t.Fatal(err)
	}
	anchor["schema_version"] = 1
	delete(anchor, "run_manifest_sha256")
	legacy, err := json.MarshalIndent(anchor, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(legacy, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	err = VerifyAnchor(runDir)
	if err == nil {
		t.Fatal("anchor schema 1 обязан отвергаться")
	}
	if !strings.Contains(err.Error(), "schema_version 1") {
		t.Fatalf("ожидалось упоминание schema_version 1, получено: %v", err)
	}
}
