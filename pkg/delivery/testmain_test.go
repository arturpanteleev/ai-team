package delivery

import (
	"os"
	"testing"
)

// Delivery state is signed with a key outside the working tree; tests keep it
// in a throwaway directory instead of the real user config.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ai-team-state-key-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("AI_TEAM_STATE_KEY_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
