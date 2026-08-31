package containment

import (
	"encoding/json"
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
