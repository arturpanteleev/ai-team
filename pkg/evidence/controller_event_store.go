//go:build linux || darwin

package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

const eventLogReservationSchema = 1
const eventLogAuthorityProofSchema = 1

var errUnreservedControllerEventData = errors.New("controller event data exists without reservation marker")

func closeEventFDQuietly(fd int) { _ = closeControllerEventFD(fd) }

func closePinnedEventLogQuietly(log *PinnedControllerEventLog) { _ = log.Close() }

type eventLogReservation struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
}

// eventLogAuthorityProof is stored below the already controller-private
// .ai-team/state/runs mount. It survives loss of either event-store root and
// prevents an event-backed run from being reclassified as a legacy/local run.
type eventLogAuthorityProof struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Origin        string `json:"origin"`
	InitialSHA256 string `json:"initial_sha256"`
}

// ControllerEventStore stores canonical event authority outside worker-visible
// .ai-team/runs. Controller operations use dirfd-relative no-follow opens.
type ControllerEventStore struct{ TargetDir string }

func (ControllerEventStore) ExternalEventAuthority() {}

// Reserve prepares the canonical event file and writes independent provenance
// before a cloud worker is spawned. Repeated calls are accepted only for the
// same intact reservation and log.
func (s ControllerEventStore) Reserve(runID string) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	want := newEventLogAuthorityProof(runID, "fresh", nil)
	created, err := writeControllerEventAuthorityProof(s.TargetDir, want)
	if err != nil {
		return err
	}
	if !created {
		return s.waitForConcurrentAuthority(runID, nil, errors.New("controller event authority proof already exists"))
	}
	return s.reserveBytes(runID, nil)
}

// MigrateLegacy verifies and copies an unreserved legacy event log byte for
// byte into controller storage. Its independent authority proof is durable
// before canonical storage is created; the event reservation marker is
// published only after the canonical log is durable. Interrupted migrations
// therefore fail closed instead of selecting the worker-visible mirror.
func (s ControllerEventStore) MigrateLegacy(runID, runDir string) error {
	return s.MigrateLegacyWithValidator(runID, runDir, nil)
}

// MigrateLegacyWithValidator applies an additional controller policy to the
// parsed events from the same byte snapshot that is copied into canonical
// storage. This binds approval checks to the exact migrated chain and avoids a
// second path read between validation and promotion.
func (s ControllerEventStore) MigrateLegacyWithValidator(runID, runDir string, validate func([]Event) error) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	if filepath.Base(filepath.Clean(runDir)) != runID {
		return errors.New("legacy event run directory identity mismatch")
	}
	cleanRunDir := filepath.Clean(runDir)
	if filepath.Base(filepath.Dir(cleanRunDir)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(cleanRunDir))) != ".ai-team" {
		return errors.New("legacy event run directory is outside .ai-team/runs")
	}
	legacy, err := controllerEventReadLegacy(s.TargetDir, runID)
	if err != nil {
		return fmt.Errorf("read legacy event log for migration: %w", err)
	}
	events, err := VerifyEventLogBytes(legacy, runID)
	if err != nil {
		return fmt.Errorf("verify legacy event log for migration: %w", err)
	}
	if _, err := replayEventsWithAttemptManifestSource(events, runID, cleanRunDir,
		ReservedAttemptManifestSource(s)); err != nil {
		return fmt.Errorf("replay legacy event log for migration: %w", err)
	}
	if validate != nil {
		if err := validate(events); err != nil {
			return fmt.Errorf("validate legacy event log before migration: %w", err)
		}
	}
	want := newEventLogAuthorityProof(runID, "legacy", legacy)
	created, err := writeControllerEventAuthorityProof(s.TargetDir, want)
	if err != nil {
		return err
	}
	if !created {
		proof, present, proofErr := readControllerEventAuthorityProof(s.TargetDir, runID)
		if proofErr != nil {
			return proofErr
		}
		if !present || proof != want {
			return errors.New("legacy event migration conflicts with existing controller authority proof")
		}
		return s.waitForConcurrentAuthority(runID, legacy, errors.New("legacy event authority proof already exists"))
	}
	return s.reserveBytes(runID, legacy)
}

func newEventLogAuthorityProof(runID, origin string, initial []byte) eventLogAuthorityProof {
	digest := sha256.Sum256(initial)
	return eventLogAuthorityProof{SchemaVersion: eventLogAuthorityProofSchema, RunID: runID, Origin: origin, InitialSHA256: fmt.Sprintf("%x", digest[:])}
}

func (s ControllerEventStore) waitForConcurrentAuthority(runID string, prefix []byte, initialErr error) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		reserved, err := s.IsReserved(runID)
		if err == nil && reserved {
			current, readErr := s.ReadBytes(runID)
			if readErr == nil && bytes.HasPrefix(current, prefix) {
				if _, verifyErr := VerifyEventLogBytes(current, runID); verifyErr == nil {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				initialErr = err
			}
			return fmt.Errorf("concurrent controller event authority did not complete: %w", initialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s ControllerEventStore) reserveBytes(runID string, contents []byte) error {
	if err := ValidateRunID(runID); err != nil {
		return err
	}
	eventRootFD, reservationRootFD, err := openControllerEventRoots(s.TargetDir, true)
	if err != nil {
		return err
	}
	defer closeEventFDQuietly(eventRootFD)
	defer closeEventFDQuietly(reservationRootFD)

	if reserved, err := controllerEventReservationState(eventRootFD, reservationRootFD, runID); err != nil {
		if errors.Is(err, errUnreservedControllerEventData) {
			return s.waitForConcurrentReservation(eventRootFD, reservationRootFD, runID, contents, err)
		}
		return err
	} else if reserved {
		log, err := s.OpenReserved(runID)
		if err != nil {
			return err
		}
		defer closePinnedEventLogQuietly(log)
		current, err := log.ReadBytes(runID)
		if err != nil {
			return err
		}
		if contents != nil && !bytes.Equal(contents, current) {
			return errors.New("legacy event migration conflicts with reserved controller data")
		}
		return nil
	}

	runFD, err := openControllerEventDirectoryAt(eventRootFD, runID, true)
	if err != nil {
		return fmt.Errorf("prepare controller event run directory: %w", err)
	}
	defer closeEventFDQuietly(runFD)
	if _, err := controllerEventStatAt(runFD, "events.jsonl"); err == nil {
		return s.waitForConcurrentReservation(eventRootFD, reservationRootFD, runID, contents,
			errors.New("unreserved controller event data already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if contents == nil {
		contents = []byte{}
	} else if len(contents) > maxEventLogSize {
		return errors.New("legacy event log exceeds size limit")
	}
	if _, err := VerifyEventLogBytes(contents, runID); err != nil && len(contents) != 0 {
		return fmt.Errorf("controller event bytes are invalid: %w", err)
	}
	if err := controllerEventWriteNewAt(runFD, "events.jsonl", contents, 0600); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.waitForConcurrentReservation(eventRootFD, reservationRootFD, runID, contents, err)
		}
		return fmt.Errorf("create canonical event log: %w", err)
	}
	want, _ := json.Marshal(eventLogReservation{SchemaVersion: eventLogReservationSchema, RunID: runID})
	want = append(want, '\n')
	if err := controllerEventWriteNewAt(reservationRootFD, runID+".json", want, 0600); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.waitForConcurrentReservation(eventRootFD, reservationRootFD, runID, contents, err)
		}
		return fmt.Errorf("reserve controller event log: %w", err)
	}
	return nil
}

// waitForConcurrentReservation handles two supported Resume/Cancel/Recover
// admissions that both observed an unreserved legacy run before either
// finished migration. The winner publishes the canonical event bytes before
// its reservation marker; the loser waits briefly for that marker and accepts
// only a valid reserved chain whose bytes retain the exact legacy prefix.
func (s ControllerEventStore) waitForConcurrentReservation(eventRootFD, reservationRootFD int, runID string, legacy []byte, initialErr error) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		reserved, err := controllerEventReservationState(eventRootFD, reservationRootFD, runID)
		if err == nil && reserved {
			current, readErr := readReservedEventBytesAt(eventRootFD, reservationRootFD, runID)
			if readErr == nil {
				if len(current) < len(legacy) || !bytes.HasPrefix(current, legacy) {
					return errors.New("concurrent controller reservation conflicts with legacy event bytes")
				}
				if _, verifyErr := VerifyEventLogBytes(current, runID); verifyErr != nil {
					return fmt.Errorf("concurrent controller reservation has an invalid event chain: %w", verifyErr)
				}
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("concurrent controller event reservation did not complete: %w", initialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readReservedEventBytesAt(eventRootFD, reservationRootFD int, runID string) ([]byte, error) {
	markerFD, err := controllerEventOpenRegularAt(reservationRootFD, runID+".json", 1024)
	if err != nil {
		return nil, err
	}
	defer closeEventFDQuietly(markerFD)
	if err := validateControllerEventReservation(markerFD, runID); err != nil {
		return nil, err
	}
	runFD, err := openControllerEventDirectoryAt(eventRootFD, runID, false)
	if err != nil {
		return nil, err
	}
	defer closeEventFDQuietly(runFD)
	eventFD, err := controllerEventOpenRegularAt(runFD, "events.jsonl", maxEventLogSize)
	if err != nil {
		return nil, err
	}
	defer closeEventFDQuietly(eventFD)
	if err := lockControllerEventFD(eventFD, false); err != nil {
		return nil, err
	}
	defer unlockControllerEventFD(eventFD)
	return controllerEventReadFD(eventFD, maxEventLogSize)
}

// IsReserved distinguishes a genuinely legacy run from damaged controller
// authority. A canonical run directory without its external marker fails
// closed rather than falling back to worker-visible events.jsonl.
func (s ControllerEventStore) IsReserved(runID string) (bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return false, err
	}
	eventRootFD, reservationRootFD, reserved, err := inspectControllerEventRoots(s.TargetDir, runID)
	if err != nil {
		return false, err
	}
	closeEventFDQuietly(eventRootFD)
	closeEventFDQuietly(reservationRootFD)
	return reserved, nil
}

// IsLegacyMigrationCandidate reports whether an explicit migration may read
// this run's worker-visible journal: there must be no event-era proof or
// per-run controller event record, and an independent controller cloud-run
// marker must exist. Ordinary readers must continue to use IsReserved and
// treat its errors as fail-closed.
func (s ControllerEventStore) IsLegacyMigrationCandidate(runID string) (bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return false, err
	}
	return controllerEventLegacyMigrationCandidate(s.TargetDir, runID)
}

// OpenReserved pins both authority roots, the per-run directory, reservation
// file, and event file before a worker is spawned. Subsequent operations never
// resolve .ai-team or state through the workspace path again.
func (s ControllerEventStore) OpenReserved(runID string) (*PinnedControllerEventLog, error) {
	if err := ValidateRunID(runID); err != nil {
		return nil, err
	}
	eventRootFD, reservationRootFD, err := openControllerEventRoots(s.TargetDir, false)
	if err != nil {
		return nil, err
	}
	reservationFD, err := controllerEventOpenRegularAt(reservationRootFD, runID+".json", 1024)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if unreservedFD, dirErr := openControllerEventDirectoryAt(eventRootFD, runID, false); dirErr == nil {
				return nil, errors.Join(errUnreservedControllerEventData, closeControllerEventFD(unreservedFD),
					closeControllerEventFD(eventRootFD), closeControllerEventFD(reservationRootFD))
			}
		}
		return nil, errors.Join(err, closeControllerEventFD(eventRootFD), closeControllerEventFD(reservationRootFD))
	}
	if err := validateControllerEventReservation(reservationFD, runID); err != nil {
		return nil, errors.Join(err, closeControllerEventFD(reservationFD), closeControllerEventFD(eventRootFD), closeControllerEventFD(reservationRootFD))
	}
	runFD, err := openControllerEventDirectoryAt(eventRootFD, runID, false)
	rootCloseErr := closeControllerEventFD(eventRootFD)
	if err != nil || rootCloseErr != nil {
		var openErr error
		var runCloseErr error
		if err != nil {
			openErr = fmt.Errorf("reserved event data directory is unavailable: %w", err)
		} else {
			runCloseErr = closeControllerEventFD(runFD)
		}
		return nil, errors.Join(openErr, rootCloseErr, runCloseErr, closeControllerEventFD(reservationFD), closeControllerEventFD(reservationRootFD))
	}
	eventFD, err := controllerEventOpenRegularAt(runFD, "events.jsonl", maxEventLogSize)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("reserved event log is unavailable: %w", err), closeControllerEventFD(runFD),
			closeControllerEventFD(reservationFD), closeControllerEventFD(reservationRootFD))
	}
	log := &PinnedControllerEventLog{runID: runID, targetDir: s.TargetDir, eventFD: eventFD, runDirFD: runFD, reservationFD: reservationFD, reservationRootFD: reservationRootFD}
	if _, err := log.Read(runID); err != nil {
		_ = log.Close()
		return nil, err
	}
	return log, nil
}

func controllerEventReservationState(eventRootFD, reservationRootFD int, runID string) (bool, error) {
	markerFD, err := controllerEventOpenRegularAt(reservationRootFD, runID+".json", 1024)
	if err == nil {
		defer closeEventFDQuietly(markerFD)
		if err := validateControllerEventReservation(markerFD, runID); err != nil {
			return false, err
		}
		runFD, err := openControllerEventDirectoryAt(eventRootFD, runID, false)
		if err != nil {
			return false, fmt.Errorf("reserved event data directory is unavailable: %w", err)
		}
		defer closeEventFDQuietly(runFD)
		fileFD, err := controllerEventOpenRegularAt(runFD, "events.jsonl", maxEventLogSize)
		if err != nil {
			return false, fmt.Errorf("reserved event log is unavailable: %w", err)
		}
		file := os.NewFile(uintptr(fileFD), "controller event log")
		data, readErr := readEventLogBytes(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return false, errors.Join(readErr, closeErr)
		}
		if len(data) > 0 {
			if _, err := VerifyEventLogBytes(data, runID); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if unreservedFD, dirErr := openControllerEventDirectoryAt(eventRootFD, runID, false); dirErr == nil {
		return false, errors.Join(errUnreservedControllerEventData, closeControllerEventFD(unreservedFD))
	} else if !errors.Is(dirErr, os.ErrNotExist) {
		return false, dirErr
	}
	return false, nil
}

func validateControllerEventReservation(fd int, runID string) error {
	data, err := controllerEventReadFD(fd, 1024)
	if err != nil {
		return fmt.Errorf("read event log reservation: %w", err)
	}
	var reservation eventLogReservation
	if err := strictjson.Unmarshal(data, 1024, &reservation); err != nil || reservation.SchemaVersion != eventLogReservationSchema || reservation.RunID != runID {
		return errors.New("event log reservation is corrupt or has mismatched identity")
	}
	return nil
}

func (s ControllerEventStore) Path(runID string) (string, error) {
	if err := ValidateRunID(runID); err != nil {
		return "", err
	}
	if s.TargetDir == "" || !filepath.IsAbs(s.TargetDir) || filepath.Clean(s.TargetDir) != s.TargetDir {
		return "", errors.New("event store target must be absolute and clean")
	}
	return filepath.Join(s.TargetDir, ".ai-team", "state", "events", runID, "events.jsonl"), nil
}

func (s ControllerEventStore) Read(runID string) ([]Event, error) {
	data, err := s.ReadBytes(runID)
	if err != nil {
		return nil, err
	}
	return VerifyEventLogBytes(data, runID)
}

func (s ControllerEventStore) ReadBytes(runID string) ([]byte, error) {
	log, err := s.OpenReserved(runID)
	if err != nil {
		return nil, err
	}
	defer closePinnedEventLogQuietly(log)
	return log.ReadBytes(runID)
}

func (s ControllerEventStore) Append(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	log, err := s.OpenReserved(runID)
	if err != nil {
		return Event{}, err
	}
	defer closePinnedEventLogQuietly(log)
	return log.Append(runID, event, expectedSequence, expectedPreviousSHA256)
}

func (s ControllerEventStore) AppendControllerEvent(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	log, err := s.OpenReserved(runID)
	if err != nil {
		return Event{}, err
	}
	defer closePinnedEventLogQuietly(log)
	return log.AppendControllerEvent(runID, event, expectedSequence, expectedPreviousSHA256)
}

// PinnedControllerEventLog holds the event and reservation authority by open
// descriptors. It remains bound to the original inodes if a worker replaces
// any workspace pathname ancestor or leaf in its mount namespace.
type PinnedControllerEventLog struct {
	runID             string
	targetDir         string
	eventFD           int
	runDirFD          int
	reservationFD     int
	reservationRootFD int
	mu                sync.Mutex
	closed            bool
}

func (l *PinnedControllerEventLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return errors.Join(closeControllerEventFD(l.eventFD), closeControllerEventFD(l.runDirFD),
		closeControllerEventFD(l.reservationFD), closeControllerEventFD(l.reservationRootFD))
}

func (l *PinnedControllerEventLog) Read(runID string) ([]Event, error) {
	data, err := l.ReadBytes(runID)
	if err != nil {
		return nil, err
	}
	return VerifyEventLogBytes(data, runID)
}

func (l *PinnedControllerEventLog) ReadBytes(runID string) ([]byte, error) {
	if err := l.checkRun(runID); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, os.ErrClosed
	}
	if err := validateControllerEventReservation(l.reservationFD, l.runID); err != nil {
		return nil, err
	}
	if err := lockControllerEventFD(l.eventFD, false); err != nil {
		return nil, err
	}
	defer unlockControllerEventFD(l.eventFD)
	copyFD, err := controllerEventDup(l.eventFD)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(copyFD), "pinned controller event log read")
	defer func() { _ = file.Close() }()
	data, err := readEventLogBytes(file)
	if err != nil {
		return nil, err
	}
	if _, err := VerifyEventLogBytes(data, runID); err != nil {
		return nil, err
	}
	return data, nil
}

func (l *PinnedControllerEventLog) Append(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	if err := l.checkRun(runID); err != nil {
		return Event{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Event{}, os.ErrClosed
	}
	if err := validateControllerEventReservation(l.reservationFD, l.runID); err != nil {
		return Event{}, err
	}
	if err := lockControllerEventFD(l.eventFD, true); err != nil {
		return Event{}, err
	}
	defer unlockControllerEventFD(l.eventFD)
	copyFD, err := controllerEventDup(l.eventFD)
	if err != nil {
		return Event{}, err
	}
	file := os.NewFile(uintptr(copyFD), "pinned controller event log append")
	defer func() { _ = file.Close() }()
	events, err := readEventLogFile(file, runID)
	if err != nil {
		return Event{}, fmt.Errorf("event log integrity: %w", err)
	}
	lastHash := chainGenesis(runID)
	if len(events) > 0 {
		lastHash = events[len(events)-1].SHA256
	}
	if uint64(len(events)) == expectedSequence+1 && len(events) > 0 &&
		event.Sequence == 0 && event.RunID == "" && event.SHA256 == "" && event.PreviousSHA256 == "" &&
		sameRetryEvent(events[len(events)-1], event, runID, expectedSequence+1, expectedPreviousSHA256) {
		return events[len(events)-1], nil
	}
	if uint64(len(events)) != expectedSequence || lastHash != expectedPreviousSHA256 {
		return Event{}, errors.New("event log changed outside current store")
	}
	event.SchemaVersion, event.Sequence, event.RunID = SchemaVersion, expectedSequence+1, runID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.PreviousSHA256 = expectedPreviousSHA256
	event.SHA256, err = eventDigest(event)
	if err != nil {
		return Event{}, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	line := append(data, '\n')
	if int64(len(line))+fileSize(file) > maxEventLogSize {
		return Event{}, errors.New("event log exceeds size limit")
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return Event{}, err
	}
	if _, err := file.Write(line); err != nil {
		return Event{}, err
	}
	if err := file.Sync(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (l *PinnedControllerEventLog) AppendControllerEvent(runID string, event Event, expectedSequence uint64, expectedPreviousSHA256 string) (Event, error) {
	if l == nil || l.runID != runID || l.reservationFD < 0 {
		return Event{}, errors.New("controller event append identity mismatch")
	}
	events, err := l.Read(runID)
	if err != nil {
		return Event{}, err
	}
	runDir := filepath.Join(l.targetDir, ".ai-team", "runs", runID)
	validated, exactRetry, err := ValidateControllerEventAppend(events, runID, runDir, event,
		expectedSequence, expectedPreviousSHA256, ReservedAttemptManifestSource{TargetDir: l.targetDir})
	if err != nil {
		return Event{}, err
	}
	if exactRetry {
		return validated, nil
	}
	return l.Append(runID, validated, expectedSequence, expectedPreviousSHA256)
}

func (l *PinnedControllerEventLog) checkRun(runID string) error {
	if l == nil || l.runID == "" || l.runID != runID {
		return errors.New("pinned event log run identity mismatch")
	}
	return nil
}

func fileSize(file *os.File) int64 {
	info, err := file.Stat()
	if err != nil {
		return maxEventLogSize
	}
	return info.Size()
}

// ReadEventLogBytesForRunDir selects the controller log for a reserved cloud
// run and the established file path for an unreserved legacy/local run.
func ReadEventLogBytesForRunDir(runDir string, runID string) ([]byte, error) {
	clean := filepath.Clean(runDir)
	if filepath.Base(clean) != runID || filepath.Base(filepath.Dir(clean)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(clean))) != ".ai-team" {
		return safeio.ReadRegularFile(filepath.Join(clean, "events.jsonl"), maxEventLogSize)
	}
	target := filepath.Dir(filepath.Dir(filepath.Dir(clean)))
	target, err := canonicalEventTarget(target)
	if err != nil {
		return nil, err
	}
	store := ControllerEventStore{TargetDir: target}
	reserved, err := store.IsReserved(runID)
	if err != nil {
		return nil, err
	}
	if reserved {
		return store.ReadBytes(runID)
	}
	return safeio.ReadRegularFile(filepath.Join(clean, "events.jsonl"), maxEventLogSize)
}

// ResolveEventLogSource selects controller authority for reserved runs and a
// no-follow file source for unreserved legacy runs.
func ResolveEventLogSource(targetDir, runDir, runID string) (EventLog, bool, error) {
	canonicalTarget, err := canonicalEventTarget(targetDir)
	if err != nil {
		return nil, false, err
	}
	store := ControllerEventStore{TargetDir: canonicalTarget}
	reserved, err := store.IsReserved(runID)
	if err != nil {
		return nil, false, err
	}
	if reserved {
		pinned, err := store.OpenReserved(runID)
		return pinned, true, err
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

// NewFileEventLog returns the local filesystem implementation for a legacy
// run. Controller callers should prefer ResolveEventLogSource.
func NewFileEventLog(path string) EventLog { return newFileEventLog(path) }

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
	store := ControllerEventStore{TargetDir: canonicalTarget}
	reserved, err := store.IsReserved(runID)
	if err != nil || !reserved {
		return nil, false, err
	}
	// The default resolver is used by one-shot verification and by Stores whose
	// lifetime is owned by a caller without a Close method. Return the
	// descriptor-owning value source instead; each Read/Append pins and closes
	// its descriptors within that operation. Long-lived worker APIs explicitly
	// use OpenReserved and own its Close lifecycle.
	return store, true, nil
}

var _ EventLog = ControllerEventStore{}
var _ EventLog = (*PinnedControllerEventLog)(nil)
