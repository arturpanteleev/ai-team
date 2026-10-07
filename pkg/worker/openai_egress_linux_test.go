//go:build linux

package worker

import (
	"errors"
	"syscall"
	"testing"
)

func TestEnsureOpenAIEgressLoopbackSetupAndFailurePaths(t *testing.T) {
	socketErr := errors.New("socket failure")
	if err := ensureOpenAIEgressLoopbackWith(func(int, int, int) (int, error) { return -1, socketErr }, nil, nil); !errors.Is(err, socketErr) {
		t.Fatalf("socket error = %v", err)
	}

	getErr := errors.New("get flags failure")
	closed := 0
	if err := ensureOpenAIEgressLoopbackWith(func(int, int, int) (int, error) { return 7, nil }, func(_ int, _ uintptr, _ *openAIEgressIfreq) error { return getErr }, func(fd int) error {
		if fd != 7 {
			t.Fatalf("closed fd = %d, want 7", fd)
		}
		closed++
		return errors.New("close errors are ignored")
	}); !errors.Is(err, getErr) {
		t.Fatalf("get flags error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("socket close calls = %d, want 1", closed)
	}

	for _, test := range []struct {
		name     string
		flags    int16
		setError error
		wantSet  bool
	}{
		{name: "already up", flags: 1},
		{name: "raise interface", wantSet: true},
		{name: "set flags error", setError: syscall.EPERM, wantSet: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []uintptr
			err := ensureOpenAIEgressLoopbackWith(func(int, int, int) (int, error) { return 9, nil }, func(_ int, operation uintptr, request *openAIEgressIfreq) error {
				calls = append(calls, operation)
				if operation == 0x8913 {
					copy(request.name[:], "lo")
					request.flags = test.flags
					return nil
				}
				if operation != 0x8914 || request.flags&1 == 0 {
					t.Fatalf("unexpected set flags request: %#x, flags %#x", operation, request.flags)
				}
				return test.setError
			}, func(int) error { return nil })
			if !errors.Is(err, test.setError) {
				t.Fatalf("error = %v, want %v", err, test.setError)
			}
			wantCalls := 1
			if test.wantSet {
				wantCalls = 2
			}
			if len(calls) != wantCalls {
				t.Fatalf("ioctl calls = %v, want %d", calls, wantCalls)
			}
		})
	}
}
