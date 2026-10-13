package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

// pathWithoutGH leaves only a fake opencode and git on PATH, so gh is absent.
func pathWithoutGH(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	withFakeOpenCodeInPATH(t)
	if _, err := exec.LookPath("gh"); err == nil {
		t.Fatal("test PATH unexpectedly contains gh")
	}
}

func deliveryChecks(report preflight.Report) map[string]preflight.Status {
	statuses := map[string]preflight.Status{}
	for _, check := range report.Checks {
		switch check.ID {
		case "delivery", "delivery_remote", "github_auth":
			statuses[check.ID] = check.Status
		}
	}
	return statuses
}

// A controller-API worker does not deliver: the controller pushes and opens
// the PR. Its preflight must pass the standard template (implementation has a
// delivery block) without gh, credentials or origin.
func TestWorkerPreflightWithControllerAPISkipsGitHubForStandardTemplate(t *testing.T) {
	dir := newGitRepo(t)
	pathWithoutGH(t)
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CLI = "opencode"
	reg := agent.NewFS(os.DirFS("../../agents"))

	report := workerPreflight(cfg, reg, dir, true).Check(context.Background())
	statuses := deliveryChecks(report)
	if !report.Ready || statuses["delivery"] != preflight.StatusPassed || statuses["delivery_remote"] != "" || statuses["github_auth"] != "" {
		t.Fatalf("controller-API worker preflight must not require gh/origin: ready=%t checks=%+v", report.Ready, report.Checks)
	}

	local := workerPreflight(cfg, reg, dir, false).Check(context.Background())
	statuses = deliveryChecks(local)
	if local.Ready || statuses["delivery_remote"] != preflight.StatusFailed || statuses["github_auth"] != preflight.StatusFailed {
		t.Fatalf("a worker that delivers itself must still require gh/origin: ready=%t checks=%+v", local.Ready, local.Checks)
	}
}

// A controller-API worker on the loopback TCP compatibility transport (no
// bubblewrap) still delivers itself: the controller routes stage delivery only
// over the Unix socket. Its preflight must keep requiring gh/origin.
func TestWorkerPreflightOverTCPControllerAPIKeepsGitHubChecks(t *testing.T) {
	dir := newGitRepo(t)
	pathWithoutGH(t)
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CLI = "opencode"
	reg := agent.NewFS(os.DirFS("../../agents"))
	job := worker.Job{SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart, RunID: "tcp-preflight-run", TargetDir: dir}

	t.Setenv(worker.WorkerAPIAddressEnv, "http://127.0.0.1:1")
	t.Setenv(worker.WorkerAPITokenEnv, "token")
	tcpPort, err := worker.NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	if workerControllerDelivers(tcpPort) {
		t.Fatal("TCP controller API without bubblewrap must not hand delivery to the controller")
	}
	report := workerPreflight(cfg, reg, dir, workerControllerDelivers(tcpPort)).Check(context.Background())
	statuses := deliveryChecks(report)
	if report.Ready || statuses["delivery_remote"] != preflight.StatusFailed || statuses["github_auth"] != preflight.StatusFailed {
		t.Fatalf("TCP controller-API worker preflight must still require gh/origin: ready=%t checks=%+v", report.Ready, report.Checks)
	}

	t.Setenv(worker.WorkerAPIAddressEnv, "http://unix")
	t.Setenv(worker.WorkerAPISocketEnv, filepath.Join(t.TempDir(), "controller.sock"))
	unixPort, err := worker.NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	if !workerControllerDelivers(unixPort) {
		t.Fatal("Unix-socket controller API must hand delivery to the controller")
	}
}

// The controller-side template preflight keeps requiring gh and origin for a
// stage with a delivery block.
func TestProjectTemplatePreflightRequiresGitHubForStageDelivery(t *testing.T) {
	dir := newGitRepo(t)
	pathWithoutGH(t)
	controlDir := filepath.Join(dir, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	template := "schema_version: 5\ntemplate: x\ntitle: X\ncli: opencode\nstages:\n" +
		"  - id: implementation\n    title: Implementation\n    function: developer\n    result: link\n    link_kind: pr\n" +
		"    executor: agent\n    agent: coder\n    delivery: {}\n"
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	report := projectTemplatePreflight{target: dir, reg: agent.NewFS(os.DirFS("../../agents"))}.Check(context.Background())
	statuses := deliveryChecks(report)
	if report.Ready || statuses["delivery_remote"] != preflight.StatusFailed || statuses["github_auth"] != preflight.StatusFailed {
		t.Fatalf("controller preflight must require gh/origin for stage delivery: ready=%t checks=%+v", report.Ready, report.Checks)
	}
}
