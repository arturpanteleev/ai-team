package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
)

// S09: adapter stages replace the model CLI; preflight must verify the
// adapter program instead and keep the CLI required for mixed configs.

func adapterChecker(t *testing.T, stages []config.TemplateStage, program string) *Checker {
	t.Helper()
	checker := testChecker(t, false)
	checker.config.Stages = stages
	checker.config.ExecutorAdapters = map[string]stageexec.AdapterSpec{"demo": {Argv: []string{program}}}
	checker.lookPath = func(name string) (string, error) {
		if name == "opencode" {
			return "", exec.ErrNotFound
		}
		return name, nil
	}
	return checker
}

func checkByID(report Report, id string) Check {
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	return Check{}
}

func TestAdapterOnlyStagesDoNotRequireAgentCLI(t *testing.T) {
	checker := adapterChecker(t, []config.TemplateStage{
		{ID: "tech", Executor: "agent", Agent: "worker", Adapter: "demo"},
	}, "./scripts/run.sh")
	script := filepath.Join(checker.target, "scripts", "run.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := checker.Check(context.Background())
	if !report.Ready {
		t.Fatalf("adapter-only config must be ready without the CLI: %+v", report.Checks)
	}
	if cli := checkByID(report, "cli"); cli.Required {
		t.Fatalf("cli must not be required: %+v", cli)
	}
	if adapter := checkByID(report, "adapter:demo"); adapter.Status != StatusPassed || !adapter.Required {
		t.Fatalf("adapter check = %+v", adapter)
	}
}

func TestAdapterProgramIsVerified(t *testing.T) {
	stages := []config.TemplateStage{{ID: "tech", Executor: "agent", Agent: "worker", Adapter: "demo"}}
	for name, tc := range map[string]struct {
		program string
		prepare func(t *testing.T, root string)
	}{
		"missing": {program: "./scripts/none.sh"},
		"not exec": {program: "./run.sh", prepare: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		"escapes":   {program: "../outside.sh"},
		"bare PATH": {program: "opencode"},
		"via link": {program: "./link.sh", prepare: func(t *testing.T, root string) {
			outside := filepath.Join(t.TempDir(), "x.sh")
			if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "link.sh")); err != nil {
				t.Skip("symlinks unavailable")
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			checker := adapterChecker(t, stages, tc.program)
			if tc.prepare != nil {
				tc.prepare(t, checker.target)
			}
			report := checker.Check(context.Background())
			if report.Ready {
				t.Fatalf("unusable adapter must not be ready: %+v", report.Checks)
			}
			if adapter := checkByID(report, "adapter:demo"); adapter.Status != StatusFailed {
				t.Fatalf("adapter check = %+v", adapter)
			}
		})
	}
}

func TestMixedStagesStillRequireAgentCLI(t *testing.T) {
	checker := adapterChecker(t, []config.TemplateStage{
		{ID: "tech", Executor: "agent", Agent: "worker", Adapter: "demo"},
		{ID: "impl", Executor: "agent", Agent: "worker"},
	}, "demo-tool")
	report := checker.Check(context.Background())
	if report.Ready {
		t.Fatalf("an internal agent stage keeps the CLI required: %+v", report.Checks)
	}
	if cli := checkByID(report, "cli"); !cli.Required || cli.Status != StatusFailed {
		t.Fatalf("cli check = %+v", cli)
	}
}

func TestDockerAdapterChecksCLIAndMountsNotTheProgram(t *testing.T) {
	stages := []config.TemplateStage{{ID: "code_review", Executor: "agent", Agent: "worker", Adapter: "demo"}}
	checker := adapterChecker(t, stages, "/bin/sh")
	checker.config.ExecutorAdapters["demo"] = stageexec.AdapterSpec{
		Argv:   []string{"/bin/sh", "/agent/review.sh"},
		Docker: &stageexec.DockerSpec{Image: "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc", Mounts: []string{"agent:/agent"}},
	}
	if check := checkByID(checker.Check(context.Background()), "adapter:demo"); check.Status != StatusFailed {
		t.Fatalf("a missing mount source must fail: %+v", check)
	}
	if err := os.MkdirAll(filepath.Join(checker.target, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if check := checkByID(checker.Check(context.Background()), "adapter:demo"); check.Status != StatusPassed {
		t.Fatalf("docker adapter with an existing mount must pass: %+v", check)
	}
	checker.lookPath = func(name string) (string, error) { return "", exec.ErrNotFound }
	if check := checkByID(checker.Check(context.Background()), "adapter:demo"); check.Status != StatusFailed {
		t.Fatalf("a missing docker CLI must fail: %+v", check)
	}
}
