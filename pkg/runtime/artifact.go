package runtime

import (
	"strings"
	"time"
)

type Artifact struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

func ReplaceVars(s, feature string) string {
	return strings.ReplaceAll(s, "{feature}", feature)
}
