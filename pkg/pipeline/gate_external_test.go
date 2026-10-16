package pipeline

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_external_test.go — external_program (S07) as a gate of the stage chain:
// the scheduler hands the invocation to pkg/gaterun and records the attempt, and
// a valid fail, a technical error and a pass stay three different outcomes. The
// Docker variant shares the runner and is covered with a real daemon in
// pkg/gaterun; here only the dispatch of the kind is checked.

func externalProgramGate(script string) workflow.GateRef {
	ref := codeGate(gatelib.GateExternalProgram, map[string]any{
		"command":         []any{"sh", "-c", script},
		"timeout_seconds": 20,
	})
	ref.Name = "demo_program"
	return ref
}

func requireProgramHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("program gate fixture requires a POSIX shell")
	}
}

const programVerdict = `cat >/dev/null; printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"%s"}\n' `

func TestExternalProgramGate_PassFailAndTechnicalErrorAreDistinct(t *testing.T) {
	requireProgramHost(t)
	pass := runCodeGate(t, externalProgramGate(programVerdict+`pass "всё хорошо"`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, pass, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)

	fail := runCodeGate(t, externalProgramGate(programVerdict+`fail "нет раздела Риски"`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, fail, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	if !strings.Contains(fail.reason, "нет раздела Риски") {
		t.Fatalf("the program's reason must reach the attempt: %q", fail.reason)
	}

	crash := runCodeGate(t, externalProgramGate(`cat >/dev/null; echo "backend down" >&2; exit 3`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, crash, workflow.GateExecutionError, "")
	if !strings.Contains(crash.reason, "backend down") {
		t.Fatalf("the failure must name what the program said: %q", crash.reason)
	}

	garbage := runCodeGate(t, externalProgramGate(`cat >/dev/null; echo "all good, trust me"`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, garbage, workflow.GateExecutionError, "")
}

func TestExternalProgramGate_ProgramSeesTheCheckedVersion(t *testing.T) {
	requireProgramHost(t)
	// The program fails exactly when the artifact it was handed contains TODO:
	// it must be given the path of the stored Markdown result.
	script := `input=$(cat); path=$(printf '%s' "$input" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p' | head -n 1);` +
		` if grep -q '^TODO' "$path"; then v=fail; else v=pass; fi;` +
		` printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"checked"}\n' "$v"`
	clean := runCodeGate(t, externalProgramGate(script), "md", "# Результат\n\nготово\n", nil, nil)
	requireOutcome(t, clean, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	todo := runCodeGate(t, externalProgramGate(script), "md", "# Результат\n\nTODO дописать\n", nil, nil)
	requireOutcome(t, todo, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
}

// The Docker variant is the same protocol under another launcher; with a real
// daemon and the pinned image already present it runs the program in a
// container and records the attempt with the docker executor kind.
func TestDockerProgramGate_RunsInTheSharedRunner(t *testing.T) {
	requireProgramHost(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if out, err := exec.Command("docker", "image", "inspect", dockerReviewImage).CombinedOutput(); err != nil {
		t.Skipf("pinned image is not present locally: %v: %s", err, strings.TrimSpace(string(out)))
	}
	docker := func(script string) workflow.GateRef {
		ref := codeGate(gatelib.GateDockerProgram, map[string]any{
			"image": dockerReviewImage, "command": []any{"/bin/sh", "-c", script}, "timeout_seconds": 60,
		})
		ref.Name = "demo_container"
		return ref
	}
	fail := runCodeGate(t, docker(programVerdict+`fail "образ сказал нет"`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, fail, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	crash := runCodeGate(t, docker(`cat >/dev/null; exit 4`), "md", "# Результат\n", nil, nil)
	requireOutcome(t, crash, workflow.GateExecutionError, "")
}
