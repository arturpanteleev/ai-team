package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunnerRequiredPassAndFailure(t *testing.T) {
	runner := Runner{TargetDir: t.TempDir()}
	passed, err := runner.Run(context.Background(), Definition{
		Name: "go-version", Class: "build", Command: []string{"go", "version"}, Policy: PolicyRequired,
	})
	if err != nil || passed.Status != StatusPassed || passed.ExitCode != 0 || passed.ToolPath == "" {
		t.Fatalf("passed check: result=%+v err=%v", passed, err)
	}
	failed, err := runner.Run(context.Background(), Definition{
		Name: "missing-tool-subcommand", Class: "unit", Command: []string{"go", "tool", "definitely-missing-ai-team-tool"}, Policy: PolicyRequired,
	})
	var requiredErr *RequiredFailureError
	if !errors.As(err, &requiredErr) || failed.Status != StatusFailed || failed.ExitCode == 0 {
		t.Fatalf("failed check: result=%+v err=%v", failed, err)
	}
}

func TestRunnerOptionalUnavailableIsExplicitlySkipped(t *testing.T) {
	result, err := (Runner{TargetDir: t.TempDir()}).Run(context.Background(), Definition{
		Name: "optional", Class: "security", Command: []string{"ai-team-tool-that-does-not-exist"}, Policy: PolicyOptional,
	})
	if err != nil || result.Status != StatusSkipped || result.Reason == "" {
		t.Fatalf("optional unavailable: result=%+v err=%v", result, err)
	}
}

func TestDefinitionRejectsEscapingWorkingDirectory(t *testing.T) {
	definition := Definition{
		Name: "escape", Class: "lint", Command: []string{"go", "version"},
		Policy: PolicyRequired, WorkingDir: filepath.Join("..", "outside"),
	}
	if err := definition.Validate(); err == nil {
		t.Fatal("escaping working_dir должен быть отклонён")
	}
}

func TestRunnerRejectsWorkingDirectorySymlinkEscape(t *testing.T) {
	target, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "escaped")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	result, err := (Runner{TargetDir: target}).Run(context.Background(), Definition{
		Name: "escape", Class: "lint", Command: []string{"go", "version"},
		Policy: PolicyRequired, WorkingDir: "escaped",
	})
	if err == nil || result.Status != StatusFailed {
		t.Fatalf("symlink escape должен быть отклонён: result=%+v err=%v", result, err)
	}
}

func TestMergeCheckOutputIsBounded(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	written, err := buffer.Write([]byte("123456"))
	if err != nil || written != 6 || buffer.String() != "1234" || !buffer.truncated {
		t.Fatalf("limited buffer: written=%d value=%q truncated=%v err=%v", written, buffer.String(), buffer.truncated, err)
	}
}

func TestGoTestAdapterRequiresExecutedTestCases(t *testing.T) {
	t.Run("real test", func(t *testing.T) {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module example.test/check\n\ngo 1.26\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "check_test.go"), []byte("package check\nimport \"testing\"\nfunc TestReal(t *testing.T) {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		result, err := (Runner{TargetDir: target}).Run(context.Background(), Definition{
			Name: "go-test", Class: "unit", Adapter: AdapterGoTest,
			Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: PolicyRequired,
		})
		if err != nil || !IsTestEvidence(result) || result.DiscoveredTests < 1 || result.PassedTests < 1 {
			t.Fatalf("typed test evidence missing: result=%+v err=%v", result, err)
		}
	})

	t.Run("no tests", func(t *testing.T) {
		target := t.TempDir()
		_ = os.WriteFile(filepath.Join(target, "go.mod"), []byte("module example.test/empty\n\ngo 1.26\n"), 0644)
		_ = os.WriteFile(filepath.Join(target, "empty.go"), []byte("package empty\n"), 0644)
		result, err := (Runner{TargetDir: target}).Run(context.Background(), Definition{
			Name: "go-test", Class: "unit", Adapter: AdapterGoTest,
			Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: PolicyRequired,
		})
		if err == nil || result.Status != StatusFailed || IsTestEvidence(result) {
			t.Fatalf("empty suite must fail closed: result=%+v err=%v", result, err)
		}
	})
}

func TestArbitraryUnitCommandIsNotTestEvidence(t *testing.T) {
	result, err := (Runner{TargetDir: t.TempDir()}).Run(context.Background(), Definition{
		Name: "self-labelled", Class: "unit", Command: []string{"go", "version"}, Policy: PolicyRequired,
	})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("command execution itself should pass: %+v %v", result, err)
	}
	if IsTestEvidence(result) {
		t.Fatal("arbitrary command must not satisfy test evidence gate")
	}
}

func TestGoTestStructuredEvidenceSurvivesHumanOutputTruncation(t *testing.T) {
	target := t.TempDir()
	_ = os.WriteFile(filepath.Join(target, "go.mod"), []byte("module example.test/large-output\n\ngo 1.26\n"), 0644)
	testSource := `package largeoutput
import (
  "strings"
  "testing"
)
func TestLargeOutput(t *testing.T) { t.Log(strings.Repeat("x", 400000)) }
`
	_ = os.WriteFile(filepath.Join(target, "large_test.go"), []byte(testSource), 0644)
	result, err := (Runner{TargetDir: target}).Run(context.Background(), Definition{
		Name: "go-test", Class: "unit", Adapter: AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: PolicyRequired,
	})
	if err != nil || !IsTestEvidence(result) || !result.Truncated || result.StructuredOutputBytes <= maxCapturedOutput || result.StructuredOutputSHA256 == "" {
		t.Fatalf("streamed structured evidence lost: result=%+v err=%v", result, err)
	}
}
