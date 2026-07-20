package runtime

import (
	"strings"
)

func ReplaceVars(s, feature string) string {
	return strings.ReplaceAll(s, "{feature}", feature)
}
