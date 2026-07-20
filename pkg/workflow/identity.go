package workflow

import (
	"regexp"
	"strings"
)

var featurePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidFeature(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.Contains(name, "..") && !strings.HasSuffix(name, ".") &&
		!strings.HasSuffix(name, ".lock") && featurePattern.MatchString(name)
}
