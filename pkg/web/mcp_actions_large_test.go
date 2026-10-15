package web

import (
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// A result larger than the 1 MiB read limit is submitted whole and stored byte
// for byte (long text arguments go up to 3 MiB), untouched by trimming.
func TestMCPLargeStageResultKeepsItsExactBytes(t *testing.T) {
	now := time.Now().UTC()
	env := newMCPActionEnv(t, []approval.PendingApproval{buildInputApproval("input-take", approval.StatusPending, now)},
		map[string]lifecycle.State{"run-input": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"}})
	content := "# Build\n\n  leading and trailing blanks stay  \n" + strings.Repeat("строка результата\n", 50_000) + "\n\n"
	if len(content) < 1<<20 {
		t.Fatalf("the fixture must exceed 1 MiB: %d", len(content))
	}
	dev := env.as(t, actorDev)
	out := dev.do("submit_stage_result", map[string]any{"run_id": "run-input", "stage_id": "build", "result": "md", "content": content})
	submission := out["submission"].(map[string]any)
	if submission["sha256"] != sha256Hex(content) {
		t.Fatalf("the stored result must hash to the submitted bytes: %v", submission["sha256"])
	}
	tooLong := strings.Repeat("x", mcpMaxLongStringArg+1)
	if failure := dev.refused("submit_stage_result", map[string]any{"run_id": "run-input", "stage_id": "build", "result": "md", "content": tooLong}); failure.Code != "bad_arguments" {
		t.Fatalf("a result beyond the limit must be refused: %+v", failure.Code)
	}
}
