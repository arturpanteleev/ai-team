//go:build !linux

package checks

import (
	"errors"
)

func NewUntrustedCommandSandbox() (CommandSandbox, error) {
	return nil, errors.New("OS-enforced untrusted project-check sandbox is unavailable on this platform")
}
