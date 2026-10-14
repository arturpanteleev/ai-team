package e2etest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

// TestE2E_AgentImplementationStageDeliversControllerPR covers B-35 through the
// real CLI binary: the coder agent (mock runtime) changes a candidate worktree,
// the controller runs delivery.require_checks, builds the delivery plan, stops
// in awaiting_plan until the exact SHA-256 is approved, then creates the PR in
// a local bare remote through a gh stub. The PR URL becomes the
// implementation stage result automatically and the attempt keeps
// executor=agent.
func TestE2E_AgentImplementationStageDeliversControllerPR(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	remote := setupAgentPRGit(t, dir)
	ghLog := installAgentPRGHStub(t, pathEnv, remote)

	if code, out := runHumanCLI(t, bin, dir, pathEnv, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	configYAML := `schema_version: 5
template: agent-pr-e2e
title: Agent PR E2E
stages:
  - id: product_spec
    title: Product specification
    function: po
    result: md
    executor: agent
    agent: analyst
    confirm: auto
  - id: tech_design
    title: Technical design
    function: architect
    result: md
    executor: agent
    agent: architect
    confirm: auto
  - id: implementation
    title: Implementation
    function: developer
    result: link
    link_kind: pr
    executor: agent
    agent: coder
    confirm: auto
    check:
      kind: hard
      rules: [pr_exists, pr_open, pr_base_branch]
    delivery:
      require_checks: [go-test]
checks:
  - name: go-test
    class: unit
    adapter: go-test-json
    command: [go, test, -json, -count=1, ./...]
    policy: required
    timeout: 5m
cli: opencode
`
	if err := os.WriteFile(filepath.Join(dir, ".ai-team", "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	const feature = "agent-pr"
	code, out := runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir,
		"--feature", feature, "--task", "Implement the agent PR feature")
	if code != 3 {
		t.Fatalf("run must stop in awaiting_plan for exact delivery approval (exit 3), got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "MOCK: agent=coder") {
		t.Fatalf("first run must execute the coder through the mock runtime:\n%s", out)
	}
	hashMatch := regexp.MustCompile(`Plan SHA-256: ([a-f0-9]{64})`).FindStringSubmatch(out)
	resumeMatch := regexp.MustCompile(`ai-team run --resume ([^ ]+) --approve-plan`).FindStringSubmatch(out)
	if len(hashMatch) != 2 || len(resumeMatch) != 2 {
		t.Fatalf("awaiting_plan output lacks plan hash or resume command:\n%s", out)
	}
	if calls := readGHLog(t, ghLog); strings.Contains(calls, "pr create") {
		t.Fatalf("PR must not be created before the delivery plan is approved:\n%s", calls)
	}
	if branches := strings.TrimSpace(runCommand(t, dir, "git", "--git-dir", remote, "branch", "--list", "ai-team/*")); branches != "" {
		t.Fatalf("nothing may be pushed before plan approval, remote has %q", branches)
	}

	// A wrong hash is rejected before the graph runs and without side effects;
	// approval is always for the exact canonical plan.
	wrongHash := strings.Repeat("0", 64)
	wrongCode, wrongOut := runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", resumeMatch[1], "--approve-plan", wrongHash)
	if wrongCode == 0 {
		t.Fatalf("resume with a mismatched plan hash must not deliver:\n%s", wrongOut)
	}
	if calls := readGHLog(t, ghLog); strings.Contains(calls, "pr create") {
		t.Fatalf("mismatched approval created a PR:\n%s", calls)
	}
	if branches := strings.TrimSpace(runCommand(t, dir, "git", "--git-dir", remote, "branch", "--list", "ai-team/*")); branches != "" {
		t.Fatalf("mismatched approval pushed to the remote: %q", branches)
	}

	code, out = runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", resumeMatch[1], "--approve-plan", hashMatch[1])
	if code != 0 {
		t.Fatalf("approved resume must deliver and complete (exit 0), got %d:\n%s", code, out)
	}
	// The approved resume continues the stopped stage from its prepared plan:
	// the coder must not run again, otherwise the delivered code would not be
	// the code whose plan the human approved.
	if strings.Contains(out, "MOCK: agent=coder") || !strings.Contains(out, "агент повторно не запускается") {
		t.Fatalf("approved resume re-ran the coder instead of continuing the approved plan:\n%s", out)
	}
	runID := resumeMatch[1]
	runDir := filepath.Join(dir, ".ai-team", "runs", runID)
	if _, err := os.Stat(runDir); err != nil {
		runs, _ := filepath.Glob(filepath.Join(dir, ".ai-team", "runs", "*"))
		if len(runs) != 1 {
			t.Fatalf("expected one immutable run, got %v", runs)
		}
		runDir, runID = runs[0], filepath.Base(runs[0])
	}

	const expectedPR = "https://example.test/org/repo/pull/35"
	calls := readGHLog(t, ghLog)
	if strings.Count(calls, "pr create") != 1 {
		t.Fatalf("controller must create exactly one PR:\n%s", calls)
	}
	link, err := os.ReadFile(artifactsDir(dir, feature, "stages", "implementation", "result.link"))
	if err != nil || strings.TrimSpace(string(link)) != expectedPR {
		t.Fatalf("implementation stage result must be the controller PR link: %q err=%v", link, err)
	}

	// Delivery happened in the controller against the bare remote; the live
	// checkout is untouched and the pushed commit contains the coder change.
	branch := "ai-team/" + feature
	if committed := strings.TrimSpace(runCommand(t, dir, "git", "--git-dir", remote, "show", "--name-only", "--format=", branch)); committed != "e2e_implementation.go" {
		t.Fatalf("delivered commit must contain exactly the coder change, got %q", committed)
	}
	message := runCommand(t, dir, "git", "--git-dir", remote, "log", "-1", "--format=%B", branch)
	if !strings.Contains(message, delivery.TrailerRunID+": "+runID) || !strings.Contains(message, delivery.TrailerRuntime+": ") {
		t.Fatalf("delivered commit lacks controller trailers:\n%s", message)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e_implementation.go")); !os.IsNotExist(err) {
		t.Fatalf("candidate source must not appear in the live checkout: %v", err)
	}

	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"delivery_plan_approved", "delivery_deferred"} {
		if !strings.Contains(string(events), `"type":"`+eventType+`"`) {
			t.Fatalf("run evidence lacks %s:\n%s", eventType, events)
		}
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(runDir, "events.jsonl"), runID)
	if err != nil {
		t.Fatalf("replay run evidence: %v", err)
	}
	var implementationAttempt string
	for _, attempt := range replayed.Attempts {
		if attempt.Stage == "implementation" && !attempt.Superseded {
			implementationAttempt = attempt.AttemptID
		}
	}
	if implementationAttempt == "" {
		t.Fatalf("implementation attempt missing from replay: %+v", replayed.Attempts)
	}
	_, manifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), runDir, runID, implementationAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Executor != "agent" || manifest.Delivery == nil || manifest.Delivery.PlanHash != hashMatch[1] {
		t.Fatalf("implementation attempt must be agent-executed and bound to the approved plan: %+v", manifest)
	}
	terminal, found, err := delivery.ReadTerminalRecord(runDir)
	if err != nil || !found || terminal.PRURL != expectedPR || terminal.StageDeliveryReceiptSHA256 == "" {
		t.Fatalf("terminal record must reference the controller stage receipt: %+v found=%t err=%v", terminal, found, err)
	}
	if code, out := runHumanCLI(t, bin, dir, pathEnv, "verify", "--target", dir, runID); code != 0 {
		t.Fatalf("completed agent PR evidence did not verify (%d):\n%s", code, out)
	}
}

func setupAgentPRGit(t *testing.T, dir string) string {
	t.Helper()
	files := map[string]string{
		".gitignore":   ".ai-team/\n",
		"go.mod":       "module example.test/e2eimplementation\n\ngo 1.26\n",
		"base.go":      "package e2eimplementation\n\nfunc Base() int { return 1 }\n",
		"base_test.go": "package e2eimplementation\n\nimport \"testing\"\n\nfunc TestBase(t *testing.T) {\n\tif Base() != 1 {\n\t\tt.Fatal(\"base\")\n\t}\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{dir, []string{"init", "-b", "master"}},
		{dir, []string{"config", "user.name", "AI Team E2E"}},
		{dir, []string{"config", "user.email", "ai-team@example.test"}},
		{dir, []string{"add", "."}},
		{dir, []string{"commit", "-m", "initial"}},
		{filepath.Dir(remote), []string{"init", "--bare", "-b", "master", remote}},
		{dir, []string{"remote", "add", "origin", remote}},
		{dir, []string{"push", "-u", "origin", "master"}},
	} {
		command := exec.Command("git", step.args...)
		command.Dir = step.dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", step.args, err, output)
		}
	}
	return remote
}

// installAgentPRGHStub replaces the generic gh mock with one that resolves the
// PR head from the bare remote, so delivery and the hard PR checks verify the
// exact pushed commit. Every invocation is appended to the returned log.
func installAgentPRGHStub(t *testing.T, pathEnv, remote string) string {
	t.Helper()
	mockDir := strings.SplitN(strings.TrimPrefix(pathEnv, "PATH="), string(os.PathListSeparator), 2)[0]
	state := t.TempDir()
	logPath := filepath.Join(state, "gh.log")
	script := `#!/bin/sh
STATE='` + state + `'
REMOTE='` + remote + `'
echo "$*" >> "$STATE/gh.log"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo "authenticated"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  head=""
  while [ $# -gt 0 ]; do
    if [ "$1" = "--head" ]; then head="$2"; fi
    shift
  done
  printf '%s' "$head" > "$STATE/head"
  echo https://example.test/org/repo/pull/35
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  if [ ! -f "$STATE/head" ]; then
    echo "no pull requests found for branch" >&2
    exit 1
  fi
  head=$(cat "$STATE/head")
  oid=$(git --git-dir "$REMOTE" rev-parse "refs/heads/$head")
  printf '{"url":"https://example.test/org/repo/pull/35","state":"OPEN","baseRefName":"master","headRefName":"%s","headRefOid":"%s"}\n' "$head" "$oid"
  exit 0
fi
echo "unexpected gh call: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(mockDir, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func readGHLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}
