//go:build !unix

package pipeline

import (
	"fmt"
	"os"
)

func requireSingleLinkBriefFile(path string, _ os.FileInfo) error {
	return fmt.Errorf("cannot verify hard-link count for business brief file %q on this platform", path)
}
