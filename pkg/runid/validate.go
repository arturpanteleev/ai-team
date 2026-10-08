// Package runid contains validation shared by controller-owned stores.
package runid

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Validate checks that a run identity is a single safe path component.
func Validate(value string) error {
	if value == "" || value == "." || value == ".." || len(value) > 255 ||
		filepath.Base(value) != value || filepath.Clean(value) != value ||
		strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("недопустимый run_id %q", value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("недопустимый run_id %q", value)
		}
	}
	return nil
}
