package e2etest

import (
	"os"
	"testing"
)

// Delivery state is signed with a key outside the working tree, and a PR
// reference is accepted only for github.com or GH_HOST; fixtures use
// https://example.test/... as the PR host. Child ai-team processes inherit both.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ai-team-state-key-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("AI_TEAM_STATE_KEY_DIR", dir)
	_ = os.Setenv("GH_HOST", "example.test")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
