//go:build !linux

package checks

import (
	"errors"
)

func NewUntrustedCommandSandbox(string, string) (CommandSandbox, error) {
	return nil, errors.New("OS-enforced untrusted project-check sandbox is unavailable on this platform")
}
