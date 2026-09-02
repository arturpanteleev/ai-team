// Package containment (V0-P1-4) defines the containment threat model, per-axis
// receipt semantics and profile configuration. Receipt — JSON-объект в
// RunManifest, фиксирующий реальное containment-состояние run для каждой из
// четырёх осей (fs, net, proc, env).
package containment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Axis — containment axis identifier.
type Axis string

const (
	AxisFS   Axis = "fs"   // filesystem: symlink reject, worktree isolation, credential deny
	AxisNet  Axis = "net"  // network: tool deny, env isolation
	AxisProc Axis = "proc" // process: process-group kill, cleanup verification
	AxisEnv  Axis = "env"  // environment: allow-list, config dir isolation, credential file deny
)

// AllAxes — ordered list of all containment axes for iteration.
var AllAxes = []Axis{AxisFS, AxisNet, AxisProc, AxisEnv}

// Level — containment enforcement level for an axis.
type Level string

const (
	LevelENFORCED    Level = "ENFORCED"    // OS-level confinement (bubblewrap, landlock, sandbox-exec)
	LevelPARTIAL     Level = "PARTIAL"     // Application-level mitigations (safeio, tool deny, env allow-list)
	LevelUNAVAILABLE Level = "UNAVAILABLE" // No mitigations for this axis
)

// ValidLevels — all recognized containment levels.
var ValidLevels = map[Level]bool{
	LevelENFORCED: true, LevelPARTIAL: true, LevelUNAVAILABLE: true,
}

// Receipt — per-axis containment status for a run. Profile determines which
// mitigations are applied; Receipt captures the actual outcome.
type Receipt struct {
	// Axes maps each containment axis to its enforcement level.
	Axes map[Axis]Level `json:"axes"`
	// Details provides per-axis mitigation flags (axis → flag → value).
	Details map[Axis]map[string]bool `json:"details,omitempty"`
	// Profile is the containment profile name (trusted-local, strict).
	Profile string `json:"profile"`
}

// Validate checks that all axes are recognized, levels are valid, and the
// profile is known. Fail-closed: unknown axis/level → error.
func (r Receipt) Validate() error {
	if r.Axes == nil {
		return errors.New("containment receipt: отсутствует axes")
	}
	if r.Profile == "" {
		return errors.New("containment receipt: пустой profile")
	}
	for _, axis := range AllAxes {
		level, ok := r.Axes[axis]
		if !ok {
			return fmt.Errorf("containment receipt: отсутствует ось %q", axis)
		}
		if !ValidLevels[level] {
			return fmt.Errorf("containment receipt: ось %q: невалидный уровень %q", axis, level)
		}
	}
	if r.Details != nil {
		for axis := range r.Details {
			valid := false
			for _, a := range AllAxes {
				if axis == a {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("containment receipt: неизвестная ось в details: %q", axis)
			}
		}
	}
	return nil
}

// UnmarshalJSON implements json.Unmarshaler with unknown field rejection.
func (r *Receipt) UnmarshalJSON(data []byte) error {
	var raw struct {
		Axes    map[Axis]Level           `json:"axes"`
		Details map[Axis]map[string]bool `json:"details,omitempty"`
		Profile string                   `json:"profile"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	r.Axes = raw.Axes
	r.Details = raw.Details
	r.Profile = raw.Profile
	return r.Validate()
}

// DefaultTrustedLocalReceipt returns the standard receipt for the
// trusted-local profile (all axes = PARTIAL).
func DefaultTrustedLocalReceipt() Receipt {
	return Receipt{
		Axes: map[Axis]Level{
			AxisFS:   LevelPARTIAL,
			AxisNet:  LevelPARTIAL,
			AxisProc: LevelPARTIAL,
			AxisEnv:  LevelPARTIAL,
		},
		Details: map[Axis]map[string]bool{
			AxisFS:   {"symlink_reject": true, "worktree_isolation": true, "credential_deny": true},
			AxisNet:  {"tool_deny": true, "env_isolation": true},
			AxisProc: {"process_group_kill": true, "cleanup_verified": true},
			AxisEnv:  {"allow_list": true, "config_dir_isolation": true, "credential_deny": true},
		},
		Profile: "trusted-local",
	}
}

// UnavailableReceipt returns a receipt with all axes UNAVAILABLE (for
// strict profile without backend or legacy runs without receipt).
func UnavailableReceipt() Receipt {
	return Receipt{
		Axes: map[Axis]Level{
			AxisFS:   LevelUNAVAILABLE,
			AxisNet:  LevelUNAVAILABLE,
			AxisProc: LevelUNAVAILABLE,
			AxisEnv:  LevelUNAVAILABLE,
		},
		Profile: "unknown",
	}
}

// HasUnavailable returns true if any axis is UNAVAILABLE.
func (r Receipt) HasUnavailable() bool {
	for _, level := range r.Axes {
		if level == LevelUNAVAILABLE {
			return true
		}
	}
	return false
}

// IsTrustedLocal returns true if the profile is trusted-local and all axes
// are at least PARTIAL (no UNAVAILABLE).
func (r Receipt) IsTrustedLocal() bool {
	return r.Profile == "trusted-local" && !r.HasUnavailable()
}
