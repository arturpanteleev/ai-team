//go:build linux || darwin

package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hard link would let the worker alias the bound clarification input from
// another pathname, so a multiply linked file is never accepted.
func TestReadQuestionAnswerMountFileRejectsHardLinkedInput(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "answer.md")
	if err := os.WriteFile(original, []byte("answer"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(dir, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readQuestionAnswerMountFile(original, maxQuestionAnswerMountBytes); err == nil || !strings.Contains(err.Error(), "exactly one filesystem link") {
		t.Fatalf("hard-linked input error = %v, want link-count rejection", err)
	}
}
