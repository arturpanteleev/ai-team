//go:build linux || darwin

package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"golang.org/x/sys/unix"
)

func openControllerEventRoots(target string, create bool) (eventRootFD, reservationRootFD int, err error) {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return -1, -1, errors.New("event store target must be absolute and clean")
	}
	canonical, canonicalErr := filepath.EvalSymlinks(target)
	if canonicalErr != nil {
		return -1, -1, canonicalErr
	}
	target = filepath.Clean(canonical)
	targetFD, err := openControllerEventPathNoFollow(target)
	if err != nil {
		return -1, -1, err
	}
	teamFD, err := openControllerEventDirectoryAtCreate(targetFD, ".ai-team", create)
	if err != nil {
		return -1, -1, errors.Join(err, closeControllerEventFD(targetFD))
	}
	stateFD, err := openControllerEventDirectoryAtCreate(teamFD, "state", create)
	closeErr := closeControllerEventFD(teamFD)
	if err != nil || closeErr != nil {
		return -1, -1, errors.Join(err, closeErr, closeControllerEventFD(targetFD))
	}
	eventRootFD, err = openControllerEventDirectoryAtCreate(stateFD, "events", create)
	if err != nil {
		return -1, -1, errors.Join(err, closeControllerEventFD(stateFD), closeControllerEventFD(targetFD))
	}
	runsFD, err := openControllerEventDirectoryAtCreate(stateFD, "runs", create)
	closeErr = closeControllerEventFD(stateFD)
	if err != nil || closeErr != nil {
		return -1, -1, errors.Join(err, closeErr, closeControllerEventFD(eventRootFD), closeControllerEventFD(targetFD))
	}
	reservationRootFD, err = openControllerEventDirectoryAtCreate(runsFD, "event-log-reservations", create)
	closeErr = closeControllerEventFD(runsFD)
	if err != nil || closeErr != nil {
		return -1, -1, errors.Join(err, closeErr, closeControllerEventFD(eventRootFD), closeControllerEventFD(targetFD))
	}
	if err := closeControllerEventFD(targetFD); err != nil {
		return -1, -1, errors.Join(err, closeControllerEventFD(eventRootFD), closeControllerEventFD(reservationRootFD))
	}
	return eventRootFD, reservationRootFD, nil
}

func openControllerEventAuthorityRoot(target string, create bool) (int, error) {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return -1, errors.New("event authority target must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return -1, err
	}
	targetFD, err := openControllerEventPathNoFollow(filepath.Clean(canonical))
	if err != nil {
		return -1, err
	}
	teamFD, err := openControllerEventDirectoryAtCreate(targetFD, ".ai-team", create)
	closeErr := closeControllerEventFD(targetFD)
	if err != nil || closeErr != nil {
		return -1, errors.Join(err, closeErr)
	}
	stateFD, err := openControllerEventDirectoryAtCreate(teamFD, "state", create)
	closeErr = closeControllerEventFD(teamFD)
	if err != nil || closeErr != nil {
		return -1, errors.Join(err, closeErr)
	}
	runsFD, err := openControllerEventDirectoryAtCreate(stateFD, "runs", create)
	closeErr = closeControllerEventFD(stateFD)
	if err != nil || closeErr != nil {
		return -1, errors.Join(err, closeErr)
	}
	rootFD, err := openControllerEventDirectoryAtCreate(runsFD, "event-log-authority", create)
	closeErr = closeControllerEventFD(runsFD)
	if err != nil || closeErr != nil {
		return -1, errors.Join(err, closeErr)
	}
	return rootFD, nil
}

func readControllerEventAuthorityProof(target, runID string) (eventLogAuthorityProof, bool, error) {
	if err := ValidateRunID(runID); err != nil {
		return eventLogAuthorityProof{}, false, err
	}
	rootFD, err := openControllerEventAuthorityRoot(target, false)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
		return eventLogAuthorityProof{}, false, nil
	}
	if err != nil {
		return eventLogAuthorityProof{}, false, err
	}
	defer closeEventFDQuietly(rootFD)
	fd, err := controllerEventOpenRegularAt(rootFD, runID+".json", 1024)
	if errors.Is(err, os.ErrNotExist) {
		return eventLogAuthorityProof{}, false, nil
	}
	if err != nil {
		return eventLogAuthorityProof{}, false, err
	}
	data, readErr := controllerEventReadFD(fd, 1024)
	closeErr := closeControllerEventFD(fd)
	if readErr != nil || closeErr != nil {
		return eventLogAuthorityProof{}, false, errors.Join(readErr, closeErr)
	}
	var proof eventLogAuthorityProof
	if err := strictjson.Unmarshal(data, 1024, &proof); err != nil || proof.SchemaVersion != eventLogAuthorityProofSchema || proof.RunID != runID ||
		(proof.Origin != "fresh" && proof.Origin != "legacy") || len(proof.InitialSHA256) != 64 {
		return eventLogAuthorityProof{}, false, errors.New("controller event authority proof is corrupt or has mismatched identity")
	}
	for _, char := range proof.InitialSHA256 {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return eventLogAuthorityProof{}, false, errors.New("controller event authority proof has invalid initial digest")
		}
	}
	return proof, true, nil
}

func writeControllerEventAuthorityProof(target string, proof eventLogAuthorityProof) (bool, error) {
	rootFD, err := openControllerEventAuthorityRoot(target, true)
	if err != nil {
		return false, err
	}
	defer closeEventFDQuietly(rootFD)
	want, err := json.Marshal(proof)
	if err != nil {
		return false, err
	}
	want = append(want, '\n')
	if err := controllerEventWriteNewAt(rootFD, proof.RunID+".json", want, 0600); err == nil {
		return true, nil
	} else if !errors.Is(err, unix.EEXIST) {
		return false, fmt.Errorf("write controller event authority proof: %w", err)
	}
	existing, present, readErr := readControllerEventAuthorityProof(target, proof.RunID)
	if readErr != nil {
		return false, readErr
	}
	if !present || existing != proof {
		return false, errors.New("controller event authority proof conflicts with requested reservation")
	}
	return false, nil
}

// inspectControllerEventRoots opens the two authority roots independently so
// IsReserved can distinguish a genuinely absent store from a partially deleted
// controller store. A missing root is tolerated only when its counterpart has
// no per-run authority record.
func inspectControllerEventRoots(target, runID string) (eventRootFD, reservationRootFD int, reserved bool, err error) {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return -1, -1, false, errors.New("event store target must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return -1, -1, false, err
	}
	target = filepath.Clean(canonical)
	eventRootFD, eventErr := openControllerEventPathNoFollow(filepath.Join(target, ".ai-team", "state", "events"))
	if errors.Is(eventErr, unix.ENOENT) || errors.Is(eventErr, os.ErrNotExist) {
		eventRootFD = -1
	} else if eventErr != nil {
		return -1, -1, false, eventErr
	}
	reservationRootFD, reservationErr := openControllerEventPathNoFollow(filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations"))
	if errors.Is(reservationErr, unix.ENOENT) || errors.Is(reservationErr, os.ErrNotExist) {
		reservationRootFD = -1
	} else if reservationErr != nil {
		closeEventFDQuietly(eventRootFD)
		return -1, -1, false, reservationErr
	}
	if eventRootFD >= 0 && reservationRootFD >= 0 {
		reserved, err = controllerEventReservationState(eventRootFD, reservationRootFD, runID)
		if err != nil {
			closeEventFDQuietly(eventRootFD)
			closeEventFDQuietly(reservationRootFD)
			return -1, -1, false, err
		}
		_, proofPresent, proofErr := readControllerEventAuthorityProof(target, runID)
		if proofErr != nil {
			closeEventFDQuietly(eventRootFD)
			closeEventFDQuietly(reservationRootFD)
			return -1, -1, false, proofErr
		}
		if proofPresent && !reserved {
			closeEventFDQuietly(eventRootFD)
			closeEventFDQuietly(reservationRootFD)
			return -1, -1, false, errors.New("controller event authority proof exists but canonical event data is missing")
		}
		if !reserved {
			otherAuthority, authorityErr := otherControllerRunAuthority(target, runID)
			if authorityErr != nil {
				closeEventFDQuietly(eventRootFD)
				closeEventFDQuietly(reservationRootFD)
				return -1, -1, false, authorityErr
			}
			if otherAuthority {
				closeEventFDQuietly(eventRootFD)
				closeEventFDQuietly(reservationRootFD)
				return -1, -1, false, errors.New("controller run markers exist without event reservation; explicit migration is required")
			}
		}
		return eventRootFD, reservationRootFD, reserved, nil
	}
	proof, proofPresent, proofErr := readControllerEventAuthorityProof(target, runID)
	if proofErr != nil {
		closeEventFDQuietly(eventRootFD)
		closeEventFDQuietly(reservationRootFD)
		return -1, -1, false, proofErr
	}
	if proofPresent {
		closeEventFDQuietly(eventRootFD)
		closeEventFDQuietly(reservationRootFD)
		return -1, -1, false, fmt.Errorf("controller event authority proof (%s) exists but an event authority root is missing", proof.Origin)
	}
	if eventRootFD < 0 && reservationRootFD < 0 {
		otherAuthority, authorityErr := otherControllerRunAuthority(target, runID)
		if authorityErr != nil {
			return -1, -1, false, authorityErr
		}
		if otherAuthority {
			return -1, -1, false, errors.New("controller run markers exist but event authority roots are missing")
		}
		return -1, -1, false, nil
	}
	if reservationRootFD >= 0 {
		markerFD, markerErr := controllerEventOpenRegularAt(reservationRootFD, runID+".json", 1024)
		if markerErr == nil {
			closeEventFDQuietly(markerFD)
		}
		closeEventFDQuietly(reservationRootFD)
		if markerErr == nil {
			return -1, -1, false, errors.New("controller event reservation exists but event root is missing")
		}
		if !errors.Is(markerErr, os.ErrNotExist) {
			return -1, -1, false, markerErr
		}
	}
	if eventRootFD >= 0 {
		_, runErr := openControllerEventDirectoryAt(eventRootFD, runID, false)
		closeEventFDQuietly(eventRootFD)
		if runErr == nil {
			return -1, -1, false, errors.New("controller event data exists but reservation root is missing")
		}
		if !errors.Is(runErr, os.ErrNotExist) {
			return -1, -1, false, runErr
		}
	}
	otherAuthority, authorityErr := otherControllerRunAuthority(target, runID)
	if authorityErr != nil {
		return -1, -1, false, authorityErr
	}
	if otherAuthority {
		return -1, -1, false, errors.New("controller run markers exist while event authority roots are partial; explicit migration is required")
	}
	return -1, -1, false, nil
}

func controllerEventLegacyMigrationCandidate(target, runID string) (bool, error) {
	proof, proofPresent, err := readControllerEventAuthorityProof(target, runID)
	if err != nil {
		return false, err
	}
	if proofPresent {
		return false, fmt.Errorf("controller event authority proof (%s) already exists", proof.Origin)
	}
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return false, errors.New("event store target must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false, err
	}
	target = filepath.Clean(canonical)
	eventRootFD, eventErr := openControllerEventPathNoFollow(filepath.Join(target, ".ai-team", "state", "events"))
	if errors.Is(eventErr, unix.ENOENT) || errors.Is(eventErr, os.ErrNotExist) {
		eventRootFD = -1
	} else if eventErr != nil {
		return false, eventErr
	}
	if eventRootFD >= 0 {
		defer closeEventFDQuietly(eventRootFD)
		if runFD, runErr := openControllerEventDirectoryAt(eventRootFD, runID, false); runErr == nil {
			closeEventFDQuietly(runFD)
			return false, nil
		} else if !errors.Is(runErr, os.ErrNotExist) {
			return false, runErr
		}
	}
	reservationRootFD, reservationErr := openControllerEventPathNoFollow(filepath.Join(target, ".ai-team", "state", "runs", "event-log-reservations"))
	if errors.Is(reservationErr, unix.ENOENT) || errors.Is(reservationErr, os.ErrNotExist) {
		reservationRootFD = -1
	} else if reservationErr != nil {
		return false, reservationErr
	}
	if reservationRootFD >= 0 {
		defer closeEventFDQuietly(reservationRootFD)
		markerFD, markerErr := controllerEventOpenRegularAt(reservationRootFD, runID+".json", 1024)
		if markerErr == nil {
			closeEventFDQuietly(markerFD)
			return false, nil
		}
		if !errors.Is(markerErr, os.ErrNotExist) {
			return false, markerErr
		}
	}
	otherAuthority, err := otherControllerRunAuthority(target, runID)
	if err != nil {
		return false, err
	}
	return otherAuthority, nil
}

func openControllerEventPathNoFollow(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(string(filepath.Separator), clean)
	if err != nil {
		return -1, errors.Join(err, closeControllerEventFD(fd))
	}
	if rel == "." {
		return fd, nil
	}
	for _, component := range splitEventPath(rel) {
		next, openErr := openControllerEventDirectoryAt(fd, component, false)
		closeErr := closeControllerEventFD(fd)
		if openErr != nil || closeErr != nil {
			return -1, errors.Join(openErr, closeErr)
		}
		fd = next
	}
	return fd, nil
}

func controllerEventReadLegacy(target, runID string) ([]byte, error) {
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, err
	}
	targetFD, err := openControllerEventPathNoFollow(filepath.Clean(canonical))
	if err != nil {
		return nil, err
	}
	teamFD, err := openControllerEventDirectoryAt(targetFD, ".ai-team", false)
	closeErr := closeControllerEventFD(targetFD)
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	runsFD, err := openControllerEventDirectoryAt(teamFD, "runs", false)
	closeErr = closeControllerEventFD(teamFD)
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	runFD, err := openControllerEventDirectoryAt(runsFD, runID, false)
	closeErr = closeControllerEventFD(runsFD)
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	var before unix.Stat_t
	if err := unix.Fstatat(runFD, "events.jsonl", &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, errors.Join(err, closeControllerEventFD(runFD))
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > maxEventLogSize {
		return nil, errors.Join(errors.New("legacy event log is not a bounded private regular file"), closeControllerEventFD(runFD))
	}
	fd, err := unix.Openat(runFD, "events.jsonl", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	closeErr = closeControllerEventFD(runFD)
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, errors.Join(err, closeControllerEventFD(fd))
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG || after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 || after.Size < 0 || after.Size > maxEventLogSize {
		return nil, errors.Join(errors.New("legacy event log changed during safe open"), closeControllerEventFD(fd))
	}
	data, readErr := controllerEventReadFD(fd, maxEventLogSize)
	return data, errors.Join(readErr, closeControllerEventFD(fd))
}

func splitEventPath(path string) []string {
	parts := make([]string, 0, 8)
	for path != "" && path != "." {
		part := filepath.Base(path)
		if part == "" || part == string(filepath.Separator) || part == "." || part == ".." {
			return nil
		}
		parts = append([]string{part}, parts...)
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return parts
}

func openControllerEventDirectoryAt(parentFD int, name string, create bool) (int, error) {
	return openControllerEventDirectoryAtCreate(parentFD, name, create)
}

func openControllerEventDirectoryAtCreate(parentFD int, name string, create bool) (int, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return -1, errors.New("invalid controller event directory component")
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err == nil || !create || !errors.Is(err, unix.ENOENT) {
		return fd, err
	}
	if err := unix.Mkdirat(parentFD, name, 0700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	} else if err := unix.Fsync(parentFD); err != nil {
		return -1, fmt.Errorf("sync controller event parent after directory creation: %w", err)
	}
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func controllerEventStatAt(directoryFD int, name string) (bool, error) {
	var info unix.Stat_t
	err := unix.Fstatat(directoryFD, name, &info, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, os.ErrNotExist
	}
	if err != nil {
		return false, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, fmt.Errorf("controller event entry %q is not a regular file", name)
	}
	return true, nil
}

func controllerEventWriteNewAt(directoryFD int, name string, data []byte, mode uint32) error {
	// Never expose a partially written proof, reservation, or event log at its
	// canonical name. An O_EXCL create of the final path prevents replacement,
	// but makes that incomplete file visible to concurrent readers before the
	// writer has finished and synced it. Stage in this private directory, sync,
	// then atomically rename the complete regular file into place with
	// no-replace semantics. The platform helper fails with EEXIST instead of
	// replacing an existing entry and does not follow a symlink at destination.
	var tempName string
	var fd int
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return fmt.Errorf("generate controller event temporary name: %w", err)
		}
		tempName = ".event-write-" + hex.EncodeToString(random)
		var openErr error
		fd, openErr = unix.Openat(directoryFD, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
		if openErr == nil {
			break
		}
		if !errors.Is(openErr, unix.EEXIST) {
			return openErr
		}
		fd = -1
	}
	if fd < 0 {
		return errors.New("could not allocate unique controller event temporary file")
	}
	file := os.NewFile(uintptr(fd), tempName)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		removeErr := unix.Unlinkat(directoryFD, tempName, 0)
		return errors.Join(writeErr, closeErr, removeErr)
	}
	if err := controllerEventRenameNewAt(directoryFD, tempName, name); err != nil {
		removeErr := unix.Unlinkat(directoryFD, tempName, 0)
		return errors.Join(err, removeErr)
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync controller event directory after creating %q: %w", name, err)
	}
	return nil
}

func controllerEventOpenRegularAt(directoryFD int, name string, limit int64) (int, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, os.ErrNotExist
		}
		return -1, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > limit {
		return -1, fmt.Errorf("controller event file %q is not a bounded private regular file", name)
	}
	fd, err := unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return -1, errors.Join(err, closeControllerEventFD(fd))
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG || after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 || after.Size < 0 || after.Size > limit {
		return -1, errors.Join(fmt.Errorf("controller event file %q changed during safe open", name), closeControllerEventFD(fd))
	}
	return fd, nil
}

func controllerEventReadFD(fd int, limit int64) ([]byte, error) {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 || info.Size < 0 || info.Size > limit {
		return nil, errors.New("controller event file is not a bounded private regular file")
	}
	buffer := make([]byte, 0, min(info.Size, limit)+1)
	chunk := make([]byte, 32<<10)
	for offset := int64(0); offset <= limit; {
		want := int64(len(chunk))
		if remaining := limit + 1 - offset; remaining < want {
			want = remaining
		}
		if want <= 0 {
			break
		}
		n, err := unix.Pread(fd, chunk[:int(want)], offset)
		buffer = append(buffer, chunk[:n]...)
		offset += int64(n)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	if int64(len(buffer)) > limit {
		return nil, errors.New("controller event file exceeds size limit")
	}
	return buffer, nil
}

func controllerEventDup(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

func lockControllerEventFD(fd int, write bool) error {
	mode := unix.LOCK_SH
	if write {
		mode = unix.LOCK_EX
	}
	return unix.Flock(fd, mode)
}

func unlockControllerEventFD(fd int) { _ = unix.Flock(fd, unix.LOCK_UN) }

func closeControllerEventFD(fd int) error {
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}
