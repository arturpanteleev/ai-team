package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularNoFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")

	if _, err := OpenRegularNoFollow(path, os.O_RDONLY, 0); err == nil {
		t.Fatal("a missing file without O_CREATE must fail")
	}
	file, err := OpenRegularNoFollow(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("a"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	// An existing file is opened again (O_CREATE alone must not fail it).
	file, err = OpenRegularNoFollow(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("existing regular file: %v", err)
	}
	_ = file.Close()

	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := OpenRegularNoFollow(link, os.O_RDONLY, 0); err == nil {
		t.Fatal("a symlink leaf must be refused")
	}
	if _, err := OpenRegularNoFollow(dir, os.O_RDONLY, 0); err == nil {
		t.Fatal("a directory must be refused")
	}
}
