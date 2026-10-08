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
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const controllerReceiptLimit = 1 << 20

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

// ValidProfiles — распознаваемые containment profiles (AUD-02). "unknown" —
// честный маркер UnavailableReceipt() для legacy-ранов/отсутствующего backend,
// а не доверенный профиль исполнения.
var ValidProfiles = map[string]bool{
	"trusted-local": true, "strict": true, "unknown": true,
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
	if !ValidProfiles[r.Profile] {
		return fmt.Errorf("containment receipt: неизвестный profile %q", r.Profile)
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

// IsEnforced — true, только если все четыре оси реально ENFORCED (AUD-02).
// PARTIAL/UNAVAILABLE/legacy-семантика не считается fail-closed исполнением.
func (r Receipt) IsEnforced() bool {
	for _, axis := range AllAxes {
		if r.Axes[axis] != LevelENFORCED {
			return false
		}
	}
	return true
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
			AxisFS:  {"symlink_reject": true, "worktree_isolation": true, "credential_deny": true},
			AxisNet: {"tool_deny": true, "env_isolation": true},
			// Process supervision attempts process-group cleanup, while each
			// invocation reports whether its bounded cleanup wait succeeded.
			// A run-level receipt cannot truthfully assert cleanup_verified for
			// every child, so it records only the configured mechanism.
			AxisProc: {"process_group_kill": true},
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

// ControllerReceiptStore persists a worker-supplied containment assertion
// outside the worker-visible run evidence tree. This records what the worker
// claims about execution; it does not establish that claim as true.
type ControllerReceiptStore struct{ TargetDir string }

func (s ControllerReceiptStore) path(runID string) (string, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return "", err
	}
	if s.TargetDir == "" {
		return "", errors.New("containment controller target is empty")
	}
	dir, err := safeio.EnsureDir(s.TargetDir, ".ai-team", "state", "containment")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

func (s ControllerReceiptStore) Write(runID string, receipt Receipt) (retErr error) {
	if err := receipt.Validate(); err != nil {
		return err
	}
	path, err := s.path(runID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > controllerReceiptLimit {
		return errors.New("containment controller record exceeds size limit")
	}
	if existing, readErr := safeio.ReadRegularFile(path, controllerReceiptLimit); readErr == nil {
		if bytes.Equal(existing, data) {
			return syncControllerReceiptDir(filepath.Dir(path))
		}
		return fmt.Errorf("controller containment receipt already exists for run %s; conflicting overwrite rejected", runID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read controller containment receipt: %w", readErr)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-containment-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove temporary controller containment receipt: %w", err))
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, controllerReceiptLimit); readErr == nil && bytes.Equal(existing, data) {
			return syncControllerReceiptDir(dir)
		}
		return fmt.Errorf("persist controller containment receipt: %w", err)
	}
	return syncControllerReceiptDir(dir)
}

func (s ControllerReceiptStore) Read(runID string) (Receipt, error) {
	path, err := s.path(runID)
	if err != nil {
		return Receipt{}, err
	}
	data, err := safeio.ReadRegularFile(path, controllerReceiptLimit)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func syncControllerReceiptDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open controller containment directory for sync: %w", err)
	}
	return errors.Join(dir.Sync(), dir.Close())
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
