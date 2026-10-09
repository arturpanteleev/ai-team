//go:build !linux && !darwin

package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuestionAnswerMountInspectionIsExplicitlyUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answer.md")
	if err := os.WriteFile(path, []byte("answer"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readQuestionAnswerMountFile(path, maxQuestionAnswerMountBytes); err == nil ||
		!strings.Contains(err.Error(), "unsupported on this platform") {
		t.Fatalf("platform without no-follow/link-count support must fail explicitly, got %v", err)
	}
}
