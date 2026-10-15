package stageexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalContentOutputThroughSymlinkIsRejected(t *testing.T) {
	executor, request := shellExecutor(t, AdapterSpec{},
		"cat >/dev/null\nprintf '{\"schema_version\":1,\"status\":\"ok\",\"outputs\":{\"result\":{\"content\":\"pwned\"}}}'\n")
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := request.Outputs[0].Path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("content written through a symlinked output path must be rejected")
	}
	if data, _ := os.ReadFile(outside); string(data) != "original" {
		t.Fatalf("symlink target was modified: %q", data)
	}
}

func TestExternalRedactsShortAllowedEnvValues(t *testing.T) {
	t.Setenv("S09_UNIT_SHORT", "ab12")
	t.Setenv("S09_UNIT_TINY", "xyz")
	executor, request := shellExecutor(t, AdapterSpec{EnvAllow: []string{"S09_UNIT_SHORT", "S09_UNIT_TINY"}},
		"cat >/dev/null\necho \"s=$S09_UNIT_SHORT t=$S09_UNIT_TINY\" >&2\nprintf '{\"schema_version\":1,\"status\":\"ok\",\"outputs\":{\"result\":{\"content\":\"x\"}}}'\n")
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(result.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "s=[redacted]") || strings.Contains(string(log), "ab12") {
		t.Fatalf("values of at least 4 bytes must be masked: %s", log)
	}
}
