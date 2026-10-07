//go:build linux

package worker

import (
	"syscall"
	"unsafe"
)

// Linux ifreq is 40 bytes on supported 64-bit architectures: a 16-byte name
// followed by a 24-byte union. The flags member occupies the first 16 bits.
type openAIEgressIfreq struct {
	name  [16]byte
	flags int16
	pad   [22]byte
}

func ensureOpenAIEgressLoopback() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var request openAIEgressIfreq
	copy(request.name[:], "lo")
	const getFlags = uintptr(0x8913) // SIOCGIFFLAGS
	const setFlags = uintptr(0x8914) // SIOCSIFFLAGS
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), getFlags, uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno
	}
	if request.flags&1 != 0 { // IFF_UP
		return nil
	}
	request.flags |= 1
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), setFlags, uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno
	}
	return nil
}
