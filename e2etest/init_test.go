package e2etest

import (
	"os"
	"path/filepath"
	"testing"
)

// Sample-project — golden-цель для eval-прогонов.
func TestSampleProjectStructure(t *testing.T) {
	dir := "sample-project"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("sample project not found")
	}
	checkFile(t, filepath.Join(dir, "main.go"))
	checkFile(t, filepath.Join(dir, "go.mod"))
}
