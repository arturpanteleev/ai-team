//go:build unix

package pipeline

import (
	"fmt"
	"os"
	"syscall"
)

func requireSingleLinkBriefFile(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify hard-link count for business brief file %q", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("business brief file %q has %d hard links; refusing controller access", path, stat.Nlink)
	}
	return nil
}
