package containment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultTrustedLocalReceipt(t *testing.T) {
	r := DefaultTrustedLocalReceipt()
	if err := r.Validate(); err != nil {
		t.Fatalf("default receipt must be valid: %v", err)
	}
	if r.Profile != "trusted-local" {
		t.Fatalf("expected trusted-local, got %q", r.Profile)
	}
	if r.HasUnavailable() {
		t.Fatal("trusted-local receipt must not have UNAVAILABLE axes")
	}
	if !r.IsTrustedLocal() {
		t.Fatal("IsTrustedLocal() should return true")
	}
	if r.Details[AxisProc]["cleanup_verified"] {
		t.Fatal("run-level receipt must not claim cleanup verification without a per-run result")
	}
}

func TestUnavailableReceipt(t *testing.T) {
	r := UnavailableReceipt()
	if err := r.Validate(); err != nil {
		t.Fatalf("unavailable receipt must be valid: %v", err)
	}
	if !r.HasUnavailable() {
		t.Fatal("unavailable receipt must have UNAVAILABLE axes")
	}
	if r.IsTrustedLocal() {
		t.Fatal("IsTrustedLocal() should return false for unavailable receipt")
	}
}

func TestReceiptValidationRejectsUnknownAxis(t *testing.T) {
	r := Receipt{
		Axes: map[Axis]Level{AxisFS: LevelPARTIAL, AxisNet: LevelPARTIAL, AxisProc: LevelPARTIAL, AxisEnv: LevelPARTIAL},
		Details: map[Axis]map[string]bool{
			AxisFS: {"ok": true}, AxisNet: {"ok": true}, AxisProc: {"ok": true}, AxisEnv: {"ok": true},
			Axis("unknown"): {"bad": true},
		},
		Profile: "trusted-local",
	}
	if err := r.Validate(); err == nil {
		t.Fatal("unknown axis in details must be rejected")
	}
}

func TestReceiptValidationRejectsUnknownLevel(t *testing.T) {
	r := Receipt{
		Axes:    map[Axis]Level{AxisFS: LevelPARTIAL, AxisNet: Level("BOGUS"), AxisProc: LevelPARTIAL, AxisEnv: LevelPARTIAL},
		Profile: "trusted-local",
	}
	if err := r.Validate(); err == nil {
		t.Fatal("unknown level must be rejected")
	}
}

func TestReceiptRoundtrip(t *testing.T) {
	r := DefaultTrustedLocalReceipt()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Receipt
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile != r.Profile || got.Axes[AxisFS] != r.Axes[AxisFS] {
		t.Fatalf("roundtrip mismatch: %+v != %+v", got, r)
	}
}

func TestReceiptRejectsUnknownJSONField(t *testing.T) {
	raw := `{"axes":{"fs":"PARTIAL","net":"PARTIAL","proc":"PARTIAL","env":"PARTIAL"},"profile":"trusted-local","unknown_field":"x"}`
	var r Receipt
	if err := json.Unmarshal([]byte(raw), &r); err == nil {
		t.Fatal("unknown JSON field must be rejected by strict decode")
	}
}

func TestReceiptPartialDetails(t *testing.T) {
	r := Receipt{
		Axes:    map[Axis]Level{AxisFS: LevelPARTIAL, AxisNet: LevelENFORCED, AxisProc: LevelUNAVAILABLE, AxisEnv: LevelPARTIAL},
		Profile: "strict",
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("mixed levels should be valid: %v", err)
	}
	if !r.HasUnavailable() {
		t.Fatal("should detect UNAVAILABLE on proc axis")
	}
}

// AUD-02: неизвестный profile обязан валидироваться в ошибку (fail-closed).
func TestReceiptValidationRejectsUnknownProfile(t *testing.T) {
	r := UnavailableReceipt()
	r.Profile = "paranoid"
	if err := r.Validate(); err == nil {
		t.Fatal("unknown profile must be rejected (AUD-02)")
	}
}

// AUD-02: IsEnforced истинно только при ENFORCED по всем осям; ONE
// PARTIAL/UNAVAILABLE axis достаточен для блокировки untrusted.
func TestReceiptIsEnforced(t *testing.T) {
	unavailable := UnavailableReceipt()
	if unavailable.IsEnforced() {
		t.Fatal("UNAVAILABLE receipt не может быть enforced")
	}
	trustedLocal := DefaultTrustedLocalReceipt()
	if trustedLocal.IsEnforced() {
		t.Fatal("PARTIAL receipt не может быть enforced")
	}
	mixed := Receipt{
		Profile: "strict",
		Axes: map[Axis]Level{
			AxisFS: LevelENFORCED, AxisNet: LevelENFORCED, AxisProc: LevelPARTIAL, AxisEnv: LevelENFORCED,
		},
	}
	if mixed.IsEnforced() {
		t.Fatal("одна PARTIAL ось ломает IsEnforced")
	}
	allEnforced := Receipt{
		Profile: "strict",
		Axes: map[Axis]Level{
			AxisFS: LevelENFORCED, AxisNet: LevelENFORCED, AxisProc: LevelENFORCED, AxisEnv: LevelENFORCED,
		},
	}
	if !allEnforced.IsEnforced() {
		t.Fatal("все четыре ENFORCED оси должны давать IsEnforced=true")
	}
}

func TestControllerReceiptStoreValidatesIdempotencyAndConflicts(t *testing.T) {
	target := t.TempDir()
	store := ControllerReceiptStore{TargetDir: target}
	receipt := DefaultTrustedLocalReceipt()
	if err := store.Write("containment-run", receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("containment-run", receipt); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	loaded, err := store.Read("containment-run")
	if err != nil || !loaded.IsTrustedLocal() {
		t.Fatalf("stored receipt: %+v, err=%v", loaded, err)
	}
	conflicting := UnavailableReceipt()
	if err := store.Write("containment-run", conflicting); err == nil {
		t.Fatal("conflicting rewrite was accepted")
	}
	if err := store.Write("../escape", receipt); err == nil {
		t.Fatal("invalid run ID was accepted")
	}
	if err := store.Write("invalid-receipt", Receipt{Profile: "unknown"}); err == nil {
		t.Fatal("invalid receipt was accepted")
	}
	path := filepath.Join(target, ".ai-team", "state", "containment", "containment-run.json")
	if err := os.WriteFile(path, []byte(`{"profile":"unknown","axes":{},"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("containment-run"); err == nil {
		t.Fatal("corrupt controller receipt was accepted")
	}
	if _, err := store.Read("missing-run"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt error=%v, want os.ErrNotExist", err)
	}
}
