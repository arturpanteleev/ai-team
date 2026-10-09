//go:build !linux && !darwin

package evidence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

var errControllerEventStoreUnsupported = errors.New("controller event store is unsupported on this platform")

const eventLogReservationSchema = 1

type ControllerEventStore struct{ TargetDir string }

func (ControllerEventStore) ExternalEventAuthority() {}

func openControllerEventRoots(string, bool) (int, int, error) {
	return -1, -1, errControllerEventStoreUnsupported
}
func controllerEventReadLegacy(string, string) ([]byte, error) {
	return nil, errControllerEventStoreUnsupported
}
func openControllerEventDirectoryAt(int, string, bool) (int, error) {
	return -1, errControllerEventStoreUnsupported
}
func controllerEventStatAt(int, string) (bool, error) {
	return false, errControllerEventStoreUnsupported
}
func controllerEventWriteNewAt(int, string, []byte, uint32) error {
	return errControllerEventStoreUnsupported
}
func controllerEventOpenRegularAt(int, string, int64) (int, error) {
	return -1, errControllerEventStoreUnsupported
}
func controllerEventReadFD(int, int64) ([]byte, error) {
	return nil, errControllerEventStoreUnsupported
}
func controllerEventDup(int) (int, error)   { return -1, errControllerEventStoreUnsupported }
func lockControllerEventFD(int, bool) error { return errControllerEventStoreUnsupported }
func unlockControllerEventFD(int)           {}
func closeControllerEventFD(int) error      { return errControllerEventStoreUnsupported }

func (s ControllerEventStore) IsReserved(runID string) (bool, error) {
	present, err := unsupportedEventAuthorityPresent(s.TargetDir, runID)
	if err != nil {
		return false, err
	}
	if present {
		return false, errControllerEventStoreUnsupported
	}
	return false, nil
}
func (s ControllerEventStore) IsLegacyMigrationCandidate(runID string) (bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return false, err
	}
	return controllerEventLegacyMigrationCandidate(s.TargetDir, runID)
}
func controllerEventLegacyMigrationCandidate(string, string) (bool, error) {
	return false, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) OpenReserved(string) (*PinnedControllerEventLog, error) {
	return nil, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) Reserve(string) error { return errControllerEventStoreUnsupported }
func (s ControllerEventStore) MigrateLegacy(string, string) error {
	return errControllerEventStoreUnsupported
}
func (s ControllerEventStore) MigrateLegacyWithValidator(string, string, func([]Event) error) error {
	return errControllerEventStoreUnsupported
}
func (s ControllerEventStore) Read(string) ([]Event, error) {
	return nil, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) ReadBytes(string) ([]byte, error) {
	return nil, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) Append(string, Event, uint64, string) (Event, error) {
	return Event{}, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) AppendControllerEvent(string, Event, uint64, string) (Event, error) {
	return Event{}, errControllerEventStoreUnsupported
}
func (s ControllerEventStore) Path(string) (string, error) {
	return "", errControllerEventStoreUnsupported
}

func (s ControllerEventStore) roots(bool) (string, string, error) {
	return "", "", errControllerEventStoreUnsupported
}

func (s ControllerEventStore) eventDir(string, bool) (string, error) {
	return "", errControllerEventStoreUnsupported
}

func (s ControllerEventStore) reservationPath(string, bool) (string, error) {
	return "", errControllerEventStoreUnsupported
}

func ReadEventLogBytesForRunDir(runDir, runID string) ([]byte, error) {
	clean := filepath.Clean(runDir)
	if filepath.Base(clean) != runID || filepath.Base(filepath.Dir(clean)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(clean))) != ".ai-team" {
		return safeio.ReadRegularFile(filepath.Join(clean, "events.jsonl"), maxEventLogSize)
	}
	target, err := canonicalEventTarget(filepath.Dir(filepath.Dir(filepath.Dir(clean))))
	if err != nil {
		return nil, err
	}
	present, err := unsupportedEventAuthorityPresent(target, runID)
	if err != nil {
		return nil, err
	}
	if present {
		return nil, errControllerEventStoreUnsupported
	}
	return safeio.ReadRegularFile(filepath.Join(clean, "events.jsonl"), maxEventLogSize)
}

func ResolveEventLogSource(targetDir, runDir, runID string) (EventLog, bool, error) {
	target, err := canonicalEventTarget(targetDir)
	if err != nil {
		return nil, false, err
	}
	present, err := unsupportedEventAuthorityPresent(target, runID)
	if err != nil {
		return nil, false, err
	}
	if present {
		return nil, false, errControllerEventStoreUnsupported
	}
	cleanRunDir := filepath.Clean(runDir)
	if filepath.Base(cleanRunDir) != runID {
		return nil, false, errors.New("event log run directory identity mismatch")
	}
	return newFileEventLog(filepath.Join(cleanRunDir, "events.jsonl")), false, nil
}

func canonicalEventTarget(target string) (string, error) {
	if target == "" {
		return "", errors.New("event store target is required")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = canonical
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func defaultEventLogSource(path, runID string) (EventLog, bool, error) {
	if filepath.Base(path) != "events.jsonl" || filepath.Base(filepath.Dir(path)) != runID {
		return nil, false, nil
	}
	clean := filepath.Clean(filepath.Dir(path))
	if filepath.Base(filepath.Dir(clean)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(clean))) != ".ai-team" {
		return nil, false, nil
	}
	target := filepath.Dir(filepath.Dir(filepath.Dir(clean)))
	canonicalTarget, err := canonicalEventTarget(target)
	if err != nil {
		return nil, false, err
	}
	present, err := unsupportedEventAuthorityPresent(canonicalTarget, runID)
	if err != nil {
		return nil, false, err
	}
	if present {
		return nil, false, errControllerEventStoreUnsupported
	}
	return newFileEventLog(path), true, nil
}

func unsupportedEventAuthorityPresent(target, runID string) (bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return false, err
	}
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return false, errors.New("event store target must be absolute and clean")
	}
	for _, path := range []string{
		filepath.Join(target, ".ai-team", "state", "runs", "event-log-authority", runID+".json"),
		filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations", runID+".json"),
		filepath.Join(target, ".ai-team", "state", "events", runID),
	} {
		_, err := os.Lstat(path)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect unsupported controller event authority: %w", err)
		}
	}
	return otherControllerRunAuthority(target, runID)
}

func NewFileEventLog(path string) EventLog { return newFileEventLog(path) }

type PinnedControllerEventLog struct{}

func (*PinnedControllerEventLog) Close() error { return errControllerEventStoreUnsupported }
func (*PinnedControllerEventLog) Read(string) ([]Event, error) {
	return nil, errControllerEventStoreUnsupported
}
func (*PinnedControllerEventLog) ReadBytes(string) ([]byte, error) {
	return nil, errControllerEventStoreUnsupported
}
func (*PinnedControllerEventLog) Append(string, Event, uint64, string) (Event, error) {
	return Event{}, errControllerEventStoreUnsupported
}
func (*PinnedControllerEventLog) AppendControllerEvent(string, Event, uint64, string) (Event, error) {
	return Event{}, errControllerEventStoreUnsupported
}
