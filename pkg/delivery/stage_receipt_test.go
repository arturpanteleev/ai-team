package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStageReceipt() StageDeliveryReceipt {
	return StageDeliveryReceipt{
		RunID: "run-35", AttemptID: "run-35-003-implementation",
		AttemptManifestSHA256: strings.Repeat("a", 64), PlanHash: strings.Repeat("b", 64),
		RuntimeIdentity: strings.Repeat("c", 64), ApprovalIdentity: "approval-1",
		Feature: "feat", CommitSHA: strings.Repeat("d", 40),
		PRURL: "https://example.test/org/repo/pull/35", PerformedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func requestFor(receipt StageDeliveryReceipt) StageDeliveryRequest {
	return StageDeliveryRequest{
		RunID: receipt.RunID, AttemptID: receipt.AttemptID, AttemptManifestSHA256: receipt.AttemptManifestSHA256,
		PlanHash: receipt.PlanHash, RuntimeIdentity: receipt.RuntimeIdentity,
	}
}

func TestStageDeliveryReceiptWriteReadIsIdempotentAndRejectsConflicts(t *testing.T) {
	target := t.TempDir()
	receipt := testStageReceipt()
	request := requestFor(receipt)
	if _, found, err := ReadControllerStageDeliveryReceipt(target, request); err != nil || found {
		t.Fatalf("missing receipt: found=%t err=%v", found, err)
	}
	if err := WriteControllerStageDeliveryReceipt(target, receipt); err != nil {
		t.Fatal(err)
	}
	stored, found, err := ReadControllerStageDeliveryReceipt(target, request)
	if err != nil || !found || stored.PRURL != receipt.PRURL || stored.ReceiptSHA256 == "" {
		t.Fatalf("stored receipt=%+v found=%t err=%v", stored, found, err)
	}
	if err := WriteControllerStageDeliveryReceipt(target, receipt); err != nil {
		t.Fatalf("exact retry must be idempotent: %v", err)
	}
	conflicting := receipt
	conflicting.PRURL = "https://example.test/org/repo/pull/36"
	if err := WriteControllerStageDeliveryReceipt(target, conflicting); err == nil {
		t.Fatal("conflicting receipt for the same identity was accepted")
	}
	other := request
	other.PlanHash = strings.Repeat("e", 64)
	if _, found, err := ReadControllerStageDeliveryReceipt(target, other); err != nil || found {
		t.Fatalf("receipt must be keyed by the full identity: found=%t err=%v", found, err)
	}

	path, err := stageReceiptPath(target, request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), "pull/35", "pull/99", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadControllerStageDeliveryReceipt(target, request); err == nil {
		t.Fatal("tampered receipt passed its self digest")
	}
	if err := os.WriteFile(path, append(data, []byte("{}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadControllerStageDeliveryReceipt(target, request); err == nil {
		t.Fatal("receipt with trailing JSON was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"unknown":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadControllerStageDeliveryReceipt(target, request); err == nil {
		t.Fatal("receipt with unknown fields was accepted")
	}
}

func TestStageDeliveryReceiptReadRejectsIdentityMismatch(t *testing.T) {
	target := t.TempDir()
	receipt := testStageReceipt()
	request := requestFor(receipt)
	sealed, err := SealStageDeliveryReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	// Store a validly sealed receipt for another attempt under this key.
	foreign := receipt
	foreign.AttemptID = "run-35-004-implementation"
	foreignSealed, err := SealStageDeliveryReceipt(foreign)
	if err != nil {
		t.Fatal(err)
	}
	path, err := stageReceiptPath(target, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema_version":1,"run_id":"run-35","attempt_id":"` + foreignSealed.AttemptID +
		`","attempt_manifest_sha256":"` + foreignSealed.AttemptManifestSHA256 + `","plan_hash":"` + foreignSealed.PlanHash +
		`","runtime_identity":"` + foreignSealed.RuntimeIdentity + `","approval_identity":"approval-1","feature":"feat","commit_sha":"` +
		foreignSealed.CommitSHA + `","pr_url":"` + foreignSealed.PRURL + `","performed_at":"` + foreignSealed.PerformedAt.Format(time.RFC3339) +
		`","receipt_sha256":"` + foreignSealed.ReceiptSHA256 + `"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadControllerStageDeliveryReceipt(target, request); err == nil {
		t.Fatal("receipt for another attempt was accepted under this request key")
	}
	if sealed.ReceiptSHA256 == foreignSealed.ReceiptSHA256 {
		t.Fatal("distinct receipts must have distinct digests")
	}
}

func TestStageDeliveryReceiptValidation(t *testing.T) {
	cases := map[string]func(*StageDeliveryReceipt){
		"run id":        func(r *StageDeliveryReceipt) { r.RunID = "../run" },
		"attempt id":    func(r *StageDeliveryReceipt) { r.AttemptID = "a/b" },
		"manifest":      func(r *StageDeliveryReceipt) { r.AttemptManifestSHA256 = "short" },
		"plan":          func(r *StageDeliveryReceipt) { r.PlanHash = "" },
		"runtime":       func(r *StageDeliveryReceipt) { r.RuntimeIdentity = strings.Repeat("z", 64) },
		"approval":      func(r *StageDeliveryReceipt) { r.ApprovalIdentity = " " },
		"feature":       func(r *StageDeliveryReceipt) { r.Feature = "" },
		"timestamp":     func(r *StageDeliveryReceipt) { r.PerformedAt = time.Time{} },
		"commit":        func(r *StageDeliveryReceipt) { r.CommitSHA = "not-a-sha" },
		"http url":      func(r *StageDeliveryReceipt) { r.PRURL = "http://example.test/pull/1" },
		"userinfo url":  func(r *StageDeliveryReceipt) { r.PRURL = "https://user@example.test/pull/1" },
		"fragment url":  func(r *StageDeliveryReceipt) { r.PRURL = "https://example.test/pull/1#x" },
		"empty pr url":  func(r *StageDeliveryReceipt) { r.PRURL = "" },
		"dot attempt":   func(r *StageDeliveryReceipt) { r.AttemptID = ".." },
		"empty attempt": func(r *StageDeliveryReceipt) { r.AttemptID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			receipt := testStageReceipt()
			mutate(&receipt)
			if _, err := SealStageDeliveryReceipt(receipt); err == nil {
				t.Fatalf("invalid receipt (%s) was sealed", name)
			}
		})
	}
	sealed, err := SealStageDeliveryReceipt(testStageReceipt())
	if err != nil {
		t.Fatal(err)
	}
	wrongSchema := sealed
	wrongSchema.SchemaVersion = 2
	if err := wrongSchema.Validate(); err == nil {
		t.Fatal("unsupported schema version was accepted")
	}
	unsealed := sealed
	unsealed.ReceiptSHA256 = ""
	if err := unsealed.Validate(); err == nil {
		t.Fatal("receipt without self digest was accepted")
	}
	if _, err := stageReceiptPath(t.TempDir(), StageDeliveryRequest{RunID: "run-35"}); err == nil {
		t.Fatal("incomplete request produced a receipt path")
	}
	if _, err := stageReceiptPath(t.TempDir(), StageDeliveryRequest{RunID: ".."}); err == nil {
		t.Fatal("invalid run id produced a receipt path")
	}
	if err := WriteControllerStageDeliveryReceipt(t.TempDir(), StageDeliveryReceipt{}); err == nil {
		t.Fatal("empty receipt was written")
	}
}

func TestTerminalRecordStageReceiptReferenceValidation(t *testing.T) {
	base := TerminalRecord{
		SchemaVersion: TerminalRecordSchemaVersion, RunID: "run-35", Feature: "feat",
		PlanHash: strings.Repeat("b", 64), CommitSHA: strings.Repeat("d", 40),
		PRURL:             "https://example.test/org/repo/pull/35",
		Trailers:          []string{TrailerRunID + ": run-35", TrailerRuntime + ": " + strings.Repeat("c", 64)},
		AttestationSHA256: strings.Repeat("f", 64), RuntimeIdentity: strings.Repeat("c", 64),
		StageAttemptID: "run-35-003-implementation", StageAttemptManifestSHA256: strings.Repeat("a", 64),
		StageDeliveryReceiptSHA256: strings.Repeat("e", 64), PerformedAt: time.Unix(1700000000, 0).UTC(),
	}
	incomplete := base
	incomplete.StageAttemptManifestSHA256 = ""
	if err := incomplete.Validate(); err == nil || !strings.Contains(err.Error(), "stage receipt") {
		t.Fatalf("incomplete stage receipt reference accepted: %v", err)
	}
	noAttestation := base
	noAttestation.AttestationSHA256 = ""
	if err := noAttestation.Validate(); err == nil || !strings.Contains(err.Error(), "stage receipt") {
		t.Fatalf("stage receipt reference without attestation accepted: %v", err)
	}
	rebound := base
	rebound.Trailers = append(append([]string(nil), base.Trailers...), TrailerAttestation+": "+strings.Repeat("f", 64))
	if err := rebound.Validate(); err == nil || !strings.Contains(err.Error(), "attestation trailer") {
		t.Fatalf("stage-delivered commit rebound to attestation trailer: %v", err)
	}
}
