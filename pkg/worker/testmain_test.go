package worker

import (
	"os"
	"testing"
)

// Delivery state is signed with a key outside the working tree; tests keep it
// in a throwaway directory instead of the real user config.
//
// The sandbox tests re-execute this test binary as helper children with a
// minimal environment (and, inside bubblewrap, a read-only filesystem). Those
// helpers must start exactly as before, so they bypass the key setup: they do
// not touch delivery state, and a failing MkdirTemp would kill them with exit
// status 2 before the helper test could report its own result.
func TestMain(m *testing.M) {
	if os.Getenv("AI_TEAM_PARENT_ENV_PROBE") == "1" || os.Getenv("AI_TEAM_BUBBLEWRAP_PROBE") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "ai-team-state-key-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("AI_TEAM_STATE_KEY_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
