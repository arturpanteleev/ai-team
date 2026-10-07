//go:build linux

package worker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestBubblewrapWorkerCannotReadControllerStateAndCanUseTarget(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("Linux CI must install bubblewrap before running worker sandbox tests:", err)
	}
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team", "controller")
	if err := os.MkdirAll(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(controlDir, "controller.db")
	dbSecret := []byte("controller-db-secret")
	if err := os.WriteFile(dbPath, dbSecret, 0600); err != nil {
		t.Fatal(err)
	}
	sidecarSecrets := map[string]string{
		"--probe-wal":     "controller-wal-secret",
		"--probe-shm":     "controller-shm-secret",
		"--probe-journal": "controller-journal-secret",
	}
	for flag, secret := range sidecarSecrets {
		suffix := strings.TrimPrefix(strings.TrimPrefix(flag, "--probe"), "-")
		path := dbPath + "-" + suffix
		if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleDir := filepath.Join(target, ".ai-team", "state", "runs")
	if err := os.MkdirAll(lifecycleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lifecycleDir, "controller-state.json"), []byte("lifecycle-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	approvalDir := filepath.Join(target, ".ai-team", "state", "approvals")
	if err := os.MkdirAll(approvalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approvalDir, "pending.json"), []byte("legacy-approval-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	candidateMetadataDir := filepath.Join(target, ".ai-team", "state", "candidates")
	if err := os.MkdirAll(candidateMetadataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateMetadataDir, "controller-sentinel.json"), []byte("candidate-metadata-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	candidateEvidenceRoot := filepath.Join(target, ".ai-team", "state", "evidence")
	probeCandidateEvidence := pipeline.CandidateEvidence{
		SchemaVersion: 1, RunID: "sandbox-probe", Purpose: "semantic_code_review",
		WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: []pipeline.CandidateFile{},
		Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
	}
	if err := writeControllerCandidateEvidence(candidateEvidenceRoot, probeCandidateEvidence.RunID, "review-candidate.json", probeCandidateEvidence); err != nil {
		t.Fatal(err)
	}
	usageDir := filepath.Join(target, ".ai-team", "state", "usage")
	if err := os.MkdirAll(usageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usageDir, "controller-sentinel.json"), []byte("usage-envelope-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	deliveryRecord := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: "sandbox-probe", Feature: "probe", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	if err := delivery.WriteControllerTerminalRecord(target, deliveryRecord.RunID, deliveryRecord); err != nil {
		t.Fatal(err)
	}
	manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := manifestStore.Reserve("sandbox-probe"); err != nil {
		t.Fatal(err)
	}
	manifestNow := time.Now().UTC()
	manifestSentinel := evidence.AttemptManifest{SchemaVersion: evidence.SchemaVersion, RunID: "sandbox-probe", AttemptID: "controller-sentinel", Stage: "probe", StageIndex: 1,
		StartedAt: manifestNow.Add(-time.Minute), FinishedAt: manifestNow, Status: "completed", Execution: "success", Decision: "continue", Outcome: "success"}
	if err := manifestStore.Write("sandbox-probe", manifestSentinel); err != nil {
		t.Fatal(err)
	}
	seedAttestation := &attest.Statement{Type: attest.StatementType, PredicateType: attest.PredicateTypeV1, Predicate: attest.Predicate{SchemaVersion: attest.PredicateSchemaVersion, RunID: "sandbox-probe"}}
	seedAttestationBytes, _ := json.Marshal(seedAttestation)
	if err := (attest.ControllerStore{TargetDir: target}).Write("sandbox-probe", seedAttestationBytes); err != nil {
		t.Fatal(err)
	}
	seedContainment := containment.DefaultTrustedLocalReceipt()
	if err := (containment.ControllerReceiptStore{TargetDir: target}).Write("sandbox-probe", seedContainment); err != nil {
		t.Fatal(err)
	}
	briefStore := pipeline.NewFileBriefStore(target)
	brief, err := briefStore.CreateInitial("sandbox-probe", "test worker filesystem boundary")
	if err != nil {
		t.Fatal(err)
	}
	candidateWorktree := filepath.Join(target, ".ai-team", "worktrees", "probe")
	if err := os.MkdirAll(candidateWorktree, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateWorktree, "visible.txt"), []byte("worktree-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "visible.txt"), []byte("target-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()
	agentDir := filepath.Join(target, ".ai-team", "agents")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "role.md")
	if err := os.WriteFile(agentPath, []byte("agent-definition"), 0600); err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(target, "sandbox-probe.json")
	t.Setenv("AI_TEAM_BUBBLEWRAP_PROBE", "1")
	// These sentinels are deliberately present in the controller's parent
	// environment. The runtime probe verifies that none crosses the worker
	// environment boundary. OPENAI_API_KEY is provider-scoped and is not part
	// of this control-plane secret assertion.
	t.Setenv("AI_TEAM_AUTH_SECRET", "probe-auth-control-secret")
	t.Setenv("AI_TEAM_SIGNING_KEY", "probe-signing-control-secret")
	t.Setenv("AI_TEAM_DB_PASSWORD", "probe-db-control-secret")
	t.Setenv("AI_TEAM_HOSTING_WRITE_TOKEN", "probe-hosting-control-secret")
	allowWorkerTestEnvironment(t, "AI_TEAM_BUBBLEWRAP_PROBE")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestBubblewrapWorkerProbeHelper$", "--", "--probe-db", dbPath,
			"--probe-wal", dbPath + "-wal", "--probe-shm", dbPath + "-shm", "--probe-journal", dbPath + "-journal",
			"--probe-host-tcp", hostListener.Addr().String(), "--probe-output", probePath},
		target, dbPath,
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
		WithAgentRegistryPaths([]string{agentDir}),
		WithLinuxBubblewrapIsolation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	fakeOpenAI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fake-openai" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, "fake-openai-ok")
	}))
	defer fakeOpenAI.Close()
	engine.openAIEgressDial = func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", strings.TrimPrefix(fakeOpenAI.URL, "https://"))
	}
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "sandbox-probe", Feature: "probe", TaskDesc: "test worker filesystem boundary", TargetDir: target,
	}); err != nil {
		t.Fatalf("bubblewrap worker invocation failed (runtime must fail closed): %v", err)
	}
	data, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	var report sandboxProbeReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid probe report %q: %v", data, err)
	}
	if report.DatabaseReadable || report.WALReadable || report.SHMReadable || report.JournalReadable || report.LifecycleReadable || report.LegacyApprovalReadable || report.CandidateMetadataReadable || report.CandidateEvidenceReadable || report.CandidateEvidenceDirectWriteSucceeded || report.UsageStateReadable || report.DeliveryStateReadable || report.DeliveryDirectWriteSucceeded || report.ContainmentStateReadable || report.ContainmentDirectWriteSucceeded {
		t.Fatalf("controller-owned state visible inside worker: %+v", report)
	}
	if report.BriefSourceReadable || !report.BriefSourceWriteSucceeded || !report.BriefAPIListReadSucceeded {
		t.Fatalf("brief source/API boundary failed: %+v", report)
	}
	briefAfter, err := os.ReadFile(filepath.Join(target, ".ai-team", "runs", "sandbox-probe", filepath.FromSlash(brief.Version.Path)))
	if err != nil || string(briefAfter) != string(brief.Content) {
		t.Fatalf("worker changed or removed durable controller brief after child exit: content=%q err=%v", briefAfter, err)
	}
	if !report.UsageAPIWriteSucceeded {
		t.Fatalf("worker could not publish usage through the controller API: %+v", report)
	}
	if !report.DeliveryAPIWriteSucceeded {
		t.Fatalf("worker could not submit terminal delivery through controller API: %+v", report)
	}
	if !report.CandidateEvidenceAPIWriteReadSucceeded {
		t.Fatalf("worker could not round-trip candidate evidence through controller API: %+v", report)
	}
	if report.AttemptManifestReadable || report.AttemptManifestDirectWriteSucceeded || !report.AttemptManifestAPIWriteReadSucceeded {
		t.Fatalf("controller attempt manifest direct/API boundary failed: %+v", report)
	}
	if _, err := manifestStore.ReadAttemptManifest("", "sandbox-probe", manifestSentinel.AttemptID); err != nil {
		t.Fatalf("worker directly modified controller attempt manifest: %v", err)
	}
	if stored, readErr := readControllerCandidateEvidence(candidateEvidenceRoot, probeCandidateEvidence.RunID, "review-candidate.json"); readErr != nil || stored.WorkspaceSHA256 != probeCandidateEvidence.WorkspaceSHA256 {
		t.Fatalf("controller candidate evidence sentinel was modified or lost: document=%+v err=%v", stored, readErr)
	}
	if report.AttestationStateReadable || report.AttestationDirectWriteSucceeded || !report.AttestationAPIWriteSucceeded {
		t.Fatalf("controller attestation isolation/API boundary failed: %+v", report)
	}
	if _, err := (attest.ControllerStore{TargetDir: target}).Read("sandbox-probe"); err != nil {
		t.Fatalf("host controller attestation was modified or lost: %v", err)
	}
	if !report.ContainmentAPIWriteSucceeded {
		t.Fatalf("worker could not submit containment receipt through controller API: %+v", report)
	}
	if _, err := (containment.ControllerReceiptStore{TargetDir: target}).Read("sandbox-probe"); err != nil {
		t.Fatalf("host controller containment receipt was modified or lost: %v", err)
	}
	if stored, found, readErr := delivery.ReadControllerTerminalRecord(target, deliveryRecord.RunID); readErr != nil || !found || stored.CommitSHA != deliveryRecord.CommitSHA {
		t.Fatalf("controller delivery sentinel changed or disappeared: record=%+v found=%v err=%v", stored, found, readErr)
	}
	if _, err := metrics.ReadUsageEnvelope(target, "sandbox-probe"); err != nil {
		t.Fatalf("controller did not retain worker usage after process exit: %v", err)
	}
	if !report.ControllerAPIReachable {
		t.Fatalf("scoped controller API unavailable over isolated network: %+v", report)
	}
	if !report.AdminControlPlaneCallRejected {
		t.Fatalf("admin/control-plane API call was not rejected by the worker API: %+v", report)
	}
	if !report.AuthSecretAbsent || !report.SigningKeyAbsent || !report.DatabasePasswordAbsent || !report.HostingWriteTokenAbsent {
		t.Fatalf("control-plane secret sentinel reached the worker environment: %+v", report)
	}
	if report.HostTCPReachable || report.OutboundTCPReachable {
		t.Fatalf("worker escaped its private network namespace: %+v", report)
	}
	if !report.TargetReadable || !report.TargetWritable || !report.WorktreeReadable || report.AgentRegistryWritable {
		t.Fatalf("unexpected workspace or agent-registry access: %+v", report)
	}
	if !report.OpenAIProxyReachable || !report.OpenAIDeniedOtherHost || !report.OpenAIDeniedOtherPort {
		t.Fatalf("OpenAI CONNECT proxy policy failed: %+v", report)
	}
}

func runOpenAIEgressProbe(t *testing.T) (bool, bool, bool) {
	t.Helper()
	socket, token := os.Getenv(openAIEgressSocketEnv), os.Getenv(openAIEgressTokenEnv)
	if socket == "" || token == "" {
		return false, false, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyURL, closeBridge, err := StartOpenAIEgressBridge(ctx, socket, token)
	if err != nil {
		return false, false, false
	}
	defer closeBridge()
	_ = os.Setenv("HTTP_PROXY", proxyURL)
	_ = os.Setenv("HTTPS_PROXY", proxyURL)
	_ = os.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")

	proxy, err := url.Parse(proxyURL)
	if err != nil {
		return false, false, false
	}
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // fake upstream only
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	allowedRequest, err := http.NewRequest(http.MethodGet, "https://api.openai.com/fake-openai", nil)
	if err != nil {
		return false, false, false
	}
	chosenProxy, err := http.ProxyFromEnvironment(allowedRequest)
	if err != nil || chosenProxy == nil || !strings.EqualFold(chosenProxy.Host, proxy.Host) {
		return false, false, false
	}
	response, err := client.Do(allowedRequest)
	if err != nil {
		return false, false, false
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	allowed := readErr == nil && response.StatusCode == http.StatusOK && string(body) == "fake-openai-ok"

	denied := func(target string) bool {
		request, reqErr := http.NewRequest(http.MethodGet, target, nil)
		if reqErr != nil {
			return false
		}
		selected, proxyErr := http.ProxyFromEnvironment(request)
		if proxyErr != nil || selected == nil || !strings.EqualFold(selected.Host, proxy.Host) {
			return false
		}
		result, requestErr := client.Do(request)
		if result != nil {
			_ = result.Body.Close()
		}
		return requestErr != nil
	}
	return allowed, denied("https://example.invalid/"), denied("https://api.openai.com:444/")
}

func TestBubblewrapRejectsHardLinkedControllerDatabaseAndSidecar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "controller.db")
	if err := os.WriteFile(dbPath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "database-alias")
	if err := os.Link(dbPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAndCheckDatabasePath(dbPath); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked database must fail closed, got %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	sidecar := dbPath + "-wal"
	if err := os.WriteFile(sidecar, []byte("wal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sidecar, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAndCheckDatabasePath(sidecar); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked sidecar must fail closed, got %v", err)
	}
}

func TestBubblewrapMasksRunAndRetainsUnixAPIOnlyWhenSocketSetupSucceeds(t *testing.T) {
	t.Run("command masks standard host sockets", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeBwrap := filepath.Join(fakeBin, "bwrap")
		if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin)
		target := makeBubblewrapTarget(t)
		home, temp := t.TempDir(), t.TempDir()
		command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
			filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + home, "TMPDIR=" + temp})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for i := 0; i+1 < len(command.Args); i++ {
			if command.Args[i] == "--tmpfs" && command.Args[i+1] == "/run" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bubblewrap command must mask standard host sockets at /run: %v", command.Args)
		}
		briefCommand, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
			filepath.Join(target, "controller.db"), "brief-mask-test", nil, []string{"HOME=" + home, "TMPDIR=" + temp})
		if err != nil {
			t.Fatal(err)
		}
		briefDir := filepath.Join(target, ".ai-team", "runs", "brief-mask-test", "brief")
		foundBriefMask := false
		for i := 0; i+1 < len(briefCommand.Args); i++ {
			if briefCommand.Args[i] == "--tmpfs" && briefCommand.Args[i+1] == briefDir {
				foundBriefMask = true
				break
			}
		}
		if !foundBriefMask {
			t.Fatalf("bubblewrap command must mask only the current run brief source: %v", briefCommand.Args)
		}
	})

	t.Run("Unix socket setup failure does not fall back to TCP or unsandboxed worker", func(t *testing.T) {
		target := makeBubblewrapTarget(t)
		longTempRoot := filepath.Join(t.TempDir(), strings.Repeat("x", 100))
		if err := os.Mkdir(longTempRoot, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", longTempRoot)
		engine, err := NewProcessEngine([]string{"/bin/true"}, target, filepath.Join(target, "controller.db"),
			WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}))
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the bubblewrap execution path without requiring the host to
		// permit namespace creation: Unix socket setup fails before bwrap lookup.
		engine.bubblewrap = true
		_, err = engine.Start(context.Background(), pipeline.RunConfig{
			RunID: "socket-setup-failure", Feature: "probe", TaskDesc: "test fail-closed socket setup", TargetDir: target,
		})
		if err == nil || !strings.Contains(err.Error(), "worker controller API") {
			t.Fatalf("controller API Unix socket failure must stop the worker invocation before egress setup, got %v", err)
		}
	})
}

func TestBubblewrapMountsControllerCandidateEvidenceReadOnly(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	for _, tc := range []struct {
		name  string
		files []string
	}{
		{name: "review candidate only", files: []string{"review-candidate.json"}},
		{name: "both candidate documents", files: []string{"review-candidate.json", "verification-candidate.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := makeBubblewrapTarget(t)
			runID := "candidate-mount-test"
			runDir := filepath.Join(target, ".ai-team", "state", "evidence", runID)
			if err := os.MkdirAll(runDir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(runDir, name), []byte("controller-owned candidate evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), runID, nil,
				[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}

			wantPaths := make(map[string]bool, len(tc.files))
			for _, name := range tc.files {
				wantPaths[filepath.Join(runDir, name)] = false
			}
			dirMounts := 0
			for i := 0; i+2 < len(command.Args); i++ {
				if command.Args[i] == "--dir" && command.Args[i+1] == runDir {
					dirMounts++
				}
				if command.Args[i] == "--ro-bind" && command.Args[i+1] == "/dev/null" {
					if _, expected := wantPaths[command.Args[i+2]]; expected {
						wantPaths[command.Args[i+2]] = true
					}
				}
			}
			if dirMounts != 1 {
				t.Fatalf("candidate evidence run directory must be recreated exactly once after its private mask; got %d mounts in %v", dirMounts, command.Args)
			}
			for path, found := range wantPaths {
				if !found {
					t.Errorf("controller candidate evidence must be masked with a read-only /dev/null bind: %s; args=%v", path, command.Args)
				}
			}
		})
	}
}

func TestBubblewrapRejectsSymlinkedControllerCandidateEvidence(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	runDir := filepath.Join(target, ".ai-team", "state", "evidence", "sandbox-test")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(outside, []byte("outside controller state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runDir, "review-candidate.json")); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil,
		[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "private worker path") || !strings.Contains(err.Error(), "must contain only regular files and directories") {
		t.Fatalf("symlinked controller candidate evidence must be rejected by private directory validation, got %v", err)
	}
}

func TestBubblewrapRejectsNonRegularControllerAttestationRecord(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	attestationDir := filepath.Join(target, ".ai-team", "state", "attestation")
	if err := os.MkdirAll(attestationDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(attestationDir, "sandbox-test.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "controller attestation path") || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("non-regular controller attestation record must fail closed, got %v", err)
	}
}

func TestBubblewrapRejectsNonRegularControllerContainmentReceipt(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	containmentDir := filepath.Join(target, ".ai-team", "state", "containment")
	if err := os.MkdirAll(containmentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(containmentDir, "sandbox-test.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "controller containment path") || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("non-regular controller containment receipt must fail closed, got %v", err)
	}
}

func TestBubblewrapRejectsUnsafeControllerContainmentDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seedState func(t *testing.T, target string)
		want      string
	}{
		{
			name: "symlinked directory",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				stateDir := filepath.Join(target, ".ai-team", "state")
				outside := filepath.Join(t.TempDir(), "containment")
				if err := os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(stateDir, "containment")); err != nil {
					t.Fatal(err)
				}
			},
			want: "prepare controller containment mount",
		},
		{
			name: "hard-linked entry",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				containmentDir := filepath.Join(target, ".ai-team", "state", "containment")
				if err := os.Mkdir(containmentDir, 0700); err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(containmentDir, "sentinel.json")
				if err := os.WriteFile(entry, []byte("controller sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(entry, filepath.Join(target, "sentinel-alias.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "hard links",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeBwrap := filepath.Join(fakeBin, "bwrap")
			if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			tc.seedState(t, target)
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe controller containment directory must fail closed with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBubblewrapRejectsUnsafeControllerAttestationDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seedState func(t *testing.T, target string)
		want      string
	}{
		{
			name: "symlinked directory",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				stateDir := filepath.Join(target, ".ai-team", "state")
				outside := filepath.Join(t.TempDir(), "attestation")
				if err := os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(stateDir, "attestation")); err != nil {
					t.Fatal(err)
				}
			},
			want: "prepare controller attestation mount",
		},
		{
			name: "hard-linked entry",
			seedState: func(t *testing.T, target string) {
				t.Helper()
				attestationDir := filepath.Join(target, ".ai-team", "state", "attestation")
				if err := os.Mkdir(attestationDir, 0700); err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(attestationDir, "sentinel.json")
				if err := os.WriteFile(entry, []byte("controller sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(entry, filepath.Join(target, "sentinel-alias.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "hard links",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			fakeBwrap := filepath.Join(fakeBin, "bwrap")
			if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin)
			target := makeBubblewrapTarget(t)
			tc.seedState(t, target)
			_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
				filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe controller attestation directory must fail closed with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBubblewrapPathAndFileValidationFailsClosed(t *testing.T) {
	t.Run("command builder rejects invalid run ids before filesystem or runtime lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		for _, runID := range []string{"", "../escape", "bad\nid"} {
			t.Run(fmt.Sprintf("run id %q", runID), func(t *testing.T) {
				_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"),
					filepath.Join(t.TempDir(), "missing-target"), "unused.db", runID, nil, nil)
				if err == nil || !strings.Contains(err.Error(), "bubblewrap run id") {
					t.Fatalf("invalid run id must be rejected before target or bwrap lookup, got %v", err)
				}
			})
		}
	})

	t.Run("missing bubblewrap", func(t *testing.T) {
		path := t.TempDir()
		t.Setenv("PATH", path)
		if err := checkBubblewrapAvailable(); err == nil || !strings.Contains(err.Error(), "requires bubblewrap") {
			t.Fatalf("missing bubblewrap must be rejected, got %v", err)
		}
		if _, err := NewProcessEngine([]string{"worker"}, makeBubblewrapTarget(t), "controller.db", WithLinuxBubblewrapIsolation()); err == nil || !strings.Contains(err.Error(), "requires bubblewrap") {
			t.Fatalf("bubblewrap option must fail closed when runtime is missing, got %v", err)
		}
	})
	t.Run("command builder rejects root before runtime lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), string(filepath.Separator), "unused.db", "sandbox-test", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "filesystem root") {
			t.Fatalf("root workspace must fail before looking up bwrap, got %v", err)
		}
	})
	t.Run("command builder reports missing runtime", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		target := makeBubblewrapTarget(t)
		_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, filepath.Join(target, "controller.db"), "sandbox-test", nil, []string{"HOME=/tmp", "TMPDIR=/tmp"})
		if err == nil || !strings.Contains(err.Error(), "bubblewrap unavailable") {
			t.Fatalf("missing bwrap runtime must fail closed, got %v", err)
		}
	})

	t.Run("invalid worker command environment and paths", func(t *testing.T) {
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is installed by Linux CI; validation cases require it on PATH")
		}
		target := makeBubblewrapTarget(t)
		worker := exec.Command("/bin/true")
		validEnv := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
		for _, tc := range []struct {
			name       string
			env        []string
			agentPaths []string
			target     string
			dbPath     string
			want       string
		}{
			{name: "missing home", env: []string{"TMPDIR=" + t.TempDir()}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "HOME and TMPDIR"},
			{name: "relative temp", env: []string{"HOME=" + t.TempDir(), "TMPDIR=relative"}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "HOME and TMPDIR"},
			{name: "missing agent registry", env: validEnv, agentPaths: []string{filepath.Join(target, "missing-agents")}, target: target, dbPath: filepath.Join(target, "controller.db"), want: "agent registry path"},
			{name: "missing lifecycle directory", env: validEnv, target: t.TempDir(), dbPath: filepath.Join(target, "controller.db"), want: "private worker path"},
			{name: "database parent unavailable", env: validEnv, target: target, dbPath: filepath.Join(target, "missing", "controller.db"), want: "resolve database parent"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := bubblewrapWorkerCommand(context.Background(), worker, tc.target, tc.dbPath, "sandbox-test", tc.agentPaths, tc.env)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q failure, got %v", tc.want, err)
				}
			})
		}
	})
}

func TestBubblewrapCommandBuilderRejectsProtectedAliasesAndApprovalSymlinks(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is installed by Linux CI; command validation requires it on PATH")
	}
	target := makeBubblewrapTarget(t)
	dbPath := filepath.Join(target, ".ai-team", "controller.db")
	if err := os.WriteFile(dbPath, []byte("controller secret"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(target, "controller-alias.db")
	if err := os.Link(dbPath, alias); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, "sandbox-test", nil, env)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("command builder must reject a DB alias before masking: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	approvalPath := filepath.Join(target, ".ai-team", "state", "approvals")
	approvalTarget := filepath.Join(t.TempDir(), "approvals")
	if err := os.Mkdir(approvalTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(approvalTarget, approvalPath); err != nil {
		t.Fatal(err)
	}
	_, err = bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target, dbPath, "sandbox-test", nil, env)
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("command builder must reject a symlinked approval directory: %v", err)
	}
}

func TestBubblewrapPrivateStateRejectsUnsafeEntries(t *testing.T) {
	t.Run("missing required directory", func(t *testing.T) {
		if err := appendPrivateDirectoryMount(&[]string{}, filepath.Join(t.TempDir(), "missing"), true); err == nil {
			t.Fatal("missing required private directory must fail closed")
		}
	})
	t.Run("missing optional directory", func(t *testing.T) {
		args := []string{}
		if err := appendPrivateDirectoryMount(&args, filepath.Join(t.TempDir(), "missing"), false); err != nil {
			t.Fatalf("missing optional private directory should be ignored: %v", err)
		}
		if len(args) != 0 {
			t.Fatalf("unexpected mount for missing optional directory: %v", args)
		}
	})
	t.Run("non-directory path", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(file, []byte("state"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := appendPrivateDirectoryMount(&[]string{}, file, true); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("non-directory state path must fail closed: %v", err)
		}
	})
	t.Run("symlink entry", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink("missing-target", filepath.Join(dir, "linked.json")); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateDirectory(dir); err == nil || !strings.Contains(err.Error(), "regular files and directories") {
			t.Fatalf("symlink state entry must fail closed: %v", err)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if err := verifyPrivateDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("missing private directory must fail closed")
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission checks are ineffective as root")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
		if err := verifyPrivateDirectory(dir); err == nil {
			t.Fatal("unreadable private directory must fail closed")
		}
	})
	t.Run("looping database symlink", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExistingPath(loop); err == nil {
			t.Fatal("looping database path must fail closed")
		}
	})
	t.Run("missing database sidecar parent", func(t *testing.T) {
		if _, err := resolveAndCheckDatabasePath(filepath.Join(t.TempDir(), "missing", "database.db")); err == nil {
			t.Fatal("missing database parent must fail closed")
		}
	})
	t.Run("missing database path with existing parent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "database.db")
		resolved, err := resolveAndCheckDatabasePath(path)
		if err != nil || resolved != path {
			t.Fatalf("missing database leaf should resolve to its canonical future path: %q, %v", resolved, err)
		}
	})
	t.Run("database directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "database")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveAndCheckDatabasePath(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("database directory must fail closed: %v", err)
		}
	})
}

func TestBubblewrapMasksOrdinaryControllerDatabase(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	target := makeBubblewrapTarget(t)
	dbPath := filepath.Join(target, ".ai-team", "controller.db")
	if err := os.WriteFile(dbPath, []byte("ordinary controller database"), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
		dbPath, "ordinary-database-mask-test", nil,
		[]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()})
	if err != nil {
		t.Fatalf("builder must allow a regular controller database without aliases: %v", err)
	}

	wantPaths := []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"}
	for _, path := range wantPaths {
		found := false
		for i := 0; i+2 < len(command.Args); i++ {
			if command.Args[i] == "--ro-bind" && command.Args[i+1] == "/dev/null" && command.Args[i+2] == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("controller database path must be masked with --ro-bind /dev/null: %s; args=%v", path, command.Args)
		}
	}
}

func makeBubblewrapTarget(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "state", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestBubblewrapRejectsMissingAndNonDirectoryWorkspace(t *testing.T) {
	if _, err := resolveBubblewrapTarget(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace must fail closed")
	}
	file := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBubblewrapTarget(file); err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("non-directory workspace must fail closed: %v", err)
	}
}

func TestBubblewrapRejectsRunOverlappingEnvironmentAndPartialOpenAIEgress(t *testing.T) {
	fakeBin := t.TempDir()
	fakeBwrap := filepath.Join(fakeBin, "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	target := makeBubblewrapTarget(t)
	worker := exec.Command("worker")
	baseEnv := []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	build := func(env []string) error {
		t.Helper()
		_, err := bubblewrapWorkerCommand(context.Background(), worker, target,
			filepath.Join(target, "controller.db"), "sandbox-test", nil, env)
		return err
	}

	t.Run("HOME cannot reopen private run mount", func(t *testing.T) {
		err := build([]string{"HOME=/run", baseEnv[1]})
		if err == nil || !strings.Contains(err.Error(), "worker HOME") || !strings.Contains(err.Error(), "overlaps /run") {
			t.Fatalf("HOME overlapping the private /run mount must fail closed, got %v", err)
		}
	})
	t.Run("TMPDIR cannot reopen private run mount", func(t *testing.T) {
		err := build([]string{baseEnv[0], "TMPDIR=/run"})
		if err == nil || !strings.Contains(err.Error(), "worker TMPDIR") || !strings.Contains(err.Error(), "overlaps /run") {
			t.Fatalf("TMPDIR overlapping the private /run mount must fail closed, got %v", err)
		}
	})
	t.Run("OpenAI socket requires capability", func(t *testing.T) {
		env := append(append([]string(nil), baseEnv...), openAIEgressSocketEnv+"=/tmp/openai-egress.sock")
		err := build(env)
		if err == nil || !strings.Contains(err.Error(), "socket and capability must be configured together") {
			t.Fatalf("partial OpenAI egress configuration must fail closed, got %v", err)
		}
	})
	t.Run("OpenAI capability requires socket", func(t *testing.T) {
		env := append(append([]string(nil), baseEnv...), openAIEgressTokenEnv+"=scoped-capability")
		err := build(env)
		if err == nil || !strings.Contains(err.Error(), "socket and capability must be configured together") {
			t.Fatalf("partial OpenAI egress configuration must fail closed, got %v", err)
		}
	})
}

func TestBubblewrapRejectsFilesystemRootAsTarget(t *testing.T) {
	for _, target := range []string{string(filepath.Separator), filepath.Join(string(filepath.Separator), ".")} {
		t.Run(target, func(t *testing.T) {
			if resolved, err := resolveBubblewrapTarget(target); err == nil {
				t.Fatalf("filesystem root target accepted as %q", resolved)
			} else if !strings.Contains(err.Error(), "filesystem root") {
				t.Fatalf("expected filesystem root rejection, got %v", err)
			}
		})
	}

	alias := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(string(filepath.Separator), alias); err != nil {
		t.Fatal(err)
	}
	if resolved, err := resolveBubblewrapTarget(alias); err == nil {
		t.Fatalf("symlink to filesystem root accepted as %q", resolved)
	} else if !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("expected symlink root rejection, got %v", err)
	}
}

func TestBubblewrapRejectsRunMountReopeningPaths(t *testing.T) {
	t.Run("missing bind source", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-registry")
		if err := rejectRunBindPath(missing, "agent registry path"); err == nil || !strings.Contains(err.Error(), "resolve agent registry path bind path") {
			t.Fatalf("missing bind source must fail before mount construction, got %v", err)
		}
	})
	t.Run("direct run target", func(t *testing.T) {
		if _, err := resolveBubblewrapTarget("/run"); err == nil || !strings.Contains(err.Error(), "/run") {
			t.Fatalf("/run workspace must be rejected before mount construction, got %v", err)
		}
	})
	t.Run("symlink target to run", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "run-target")
		if err := os.Symlink("/run", alias); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveBubblewrapTarget(alias); err == nil || !strings.Contains(err.Error(), "/run") {
			t.Fatalf("symlink target resolving to /run must be rejected, got %v", err)
		}
	})
	t.Run("agent registry bind cannot overlap run", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeBwrap := filepath.Join(fakeBin, "bwrap")
		if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin)
		alias := filepath.Join(t.TempDir(), "root-agent-registry")
		if err := os.Symlink("/", alias); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			path string
		}{
			{name: "run", path: "/run"},
			{name: "filesystem root", path: "/"},
			{name: "symlink to filesystem root", path: alias},
		} {
			t.Run(tc.name, func(t *testing.T) {
				target := makeBubblewrapTarget(t)
				home, temp := t.TempDir(), t.TempDir()
				_, err := bubblewrapWorkerCommand(context.Background(), exec.Command("worker"), target,
					filepath.Join(target, "controller.db"), "sandbox-test", []string{tc.path}, []string{"HOME=" + home, "TMPDIR=" + temp})
				if err == nil || !strings.Contains(err.Error(), "agent registry path") || !strings.Contains(err.Error(), "overlaps /run") {
					t.Fatalf("agent registry bind %q must be rejected, got %v", tc.path, err)
				}
			})
		}
	})
}

func TestBubblewrapRejectsHardLinkedPrivateState(t *testing.T) {
	target := t.TempDir()
	privateDir := filepath.Join(target, "private")
	if err := os.Mkdir(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(privateDir, "run.json")
	if err := os.WriteFile(statePath, []byte("controller state"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(target, "visible-alias.json")
	if err := os.Link(statePath, alias); err != nil {
		t.Fatal(err)
	}
	args := []string{"--ro-bind", "/", "/"}
	if err := appendPrivateDirectoryMount(&args, privateDir, true); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked private state must fail closed, got %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("failed mount must not be appended, args=%v", args)
	}
}

type sandboxProbeReport struct {
	DatabaseReadable                       bool `json:"database_readable"`
	WALReadable                            bool `json:"wal_readable"`
	SHMReadable                            bool `json:"shm_readable"`
	JournalReadable                        bool `json:"journal_readable"`
	LifecycleReadable                      bool `json:"lifecycle_readable"`
	LegacyApprovalReadable                 bool `json:"legacy_approval_readable"`
	CandidateMetadataReadable              bool `json:"candidate_metadata_readable"`
	CandidateEvidenceReadable              bool `json:"candidate_evidence_readable"`
	CandidateEvidenceDirectWriteSucceeded  bool `json:"candidate_evidence_direct_write_succeeded"`
	CandidateEvidenceAPIWriteReadSucceeded bool `json:"candidate_evidence_api_write_read_succeeded"`
	AttemptManifestReadable                bool `json:"attempt_manifest_readable"`
	AttemptManifestDirectWriteSucceeded    bool `json:"attempt_manifest_direct_write_succeeded"`
	AttemptManifestAPIWriteReadSucceeded   bool `json:"attempt_manifest_api_write_read_succeeded"`
	UsageStateReadable                     bool `json:"usage_state_readable"`
	DeliveryStateReadable                  bool `json:"delivery_state_readable"`
	DeliveryDirectWriteSucceeded           bool `json:"delivery_direct_write_succeeded"`
	DeliveryAPIWriteSucceeded              bool `json:"delivery_api_write_succeeded"`
	AttestationStateReadable               bool `json:"attestation_state_readable"`
	AttestationDirectWriteSucceeded        bool `json:"attestation_direct_write_succeeded"`
	AttestationAPIWriteSucceeded           bool `json:"attestation_api_write_succeeded"`
	ContainmentStateReadable               bool `json:"containment_state_readable"`
	ContainmentDirectWriteSucceeded        bool `json:"containment_direct_write_succeeded"`
	ContainmentAPIWriteSucceeded           bool `json:"containment_api_write_succeeded"`
	UsageAPIWriteSucceeded                 bool `json:"usage_api_write_succeeded"`
	BriefSourceReadable                    bool `json:"brief_source_readable"`
	BriefSourceWriteSucceeded              bool `json:"brief_source_write_succeeded"`
	BriefAPIListReadSucceeded              bool `json:"brief_api_list_read_succeeded"`
	WorktreeReadable                       bool `json:"worktree_readable"`
	TargetReadable                         bool `json:"target_readable"`
	TargetWritable                         bool `json:"target_writable"`
	AgentRegistryWritable                  bool `json:"agent_registry_writable"`
	ControllerAPIReachable                 bool `json:"controller_api_reachable"`
	AdminControlPlaneCallRejected          bool `json:"admin_control_plane_call_rejected"`
	AuthSecretAbsent                       bool `json:"auth_secret_absent"`
	SigningKeyAbsent                       bool `json:"signing_key_absent"`
	DatabasePasswordAbsent                 bool `json:"database_password_absent"`
	HostingWriteTokenAbsent                bool `json:"hosting_write_token_absent"`
	HostTCPReachable                       bool `json:"host_tcp_reachable"`
	OutboundTCPReachable                   bool `json:"outbound_tcp_reachable"`
	OpenAIProxyReachable                   bool `json:"openai_proxy_reachable"`
	OpenAIDeniedOtherHost                  bool `json:"openai_denied_other_host"`
	OpenAIDeniedOtherPort                  bool `json:"openai_denied_other_port"`
}

// TestBubblewrapWorkerProbeHelper is executed as the child command by the
// integration test. It reports only whether protected sentinels were
// readable, then emits the normal worker result protocol.
func TestBubblewrapWorkerProbeHelper(t *testing.T) {
	if os.Getenv("AI_TEAM_BUBBLEWRAP_PROBE") != "1" {
		return
	}
	args := argsAfterDoubleDash(os.Args)
	value := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	job, err := DecodeJob(os.Stdin, value("--target"))
	if err != nil {
		t.Fatalf("decode probe job: %v", err)
	}
	dbData, dbErr := os.ReadFile(value("--probe-db"))
	walData, walErr := os.ReadFile(value("--probe-wal"))
	shmData, shmErr := os.ReadFile(value("--probe-shm"))
	journalData, journalErr := os.ReadFile(value("--probe-journal"))
	lifecycleData, lifecycleErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "runs", "controller-state.json"))
	approvalData, approvalErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "approvals", "pending.json"))
	candidateData, candidateErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "candidates", "controller-sentinel.json"))
	candidateEvidencePath := filepath.Join(job.TargetDir, ".ai-team", "state", "evidence", job.RunID, "review-candidate.json")
	candidateEvidenceData, candidateEvidenceErr := os.ReadFile(candidateEvidencePath)
	candidateEvidenceDirectWriteErr := os.WriteFile(candidateEvidencePath, []byte("worker-overwrite-attempt"), 0600)
	usageData, usageErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "state", "usage", "controller-sentinel.json"))
	attemptManifestPath := filepath.Join(job.TargetDir, ".ai-team", "state", "attempt-manifests", job.RunID, "controller-sentinel.json")
	attemptManifestData, attemptManifestErr := os.ReadFile(attemptManifestPath)
	attemptManifestDirectWriteErr := os.WriteFile(attemptManifestPath, []byte("worker-overwrite-attempt"), 0600)
	deliveryPath := filepath.Join(job.TargetDir, ".ai-team", "state", "delivery", job.RunID+".json")
	deliveryData, deliveryErr := os.ReadFile(deliveryPath)
	deliveryDirectWriteErr := os.WriteFile(deliveryPath, []byte("worker-overwrite-attempt"), 0600)
	attestationPath := filepath.Join(job.TargetDir, ".ai-team", "state", "attestation", job.RunID+".json")
	attestationData, attestationErr := os.ReadFile(attestationPath)
	attestationDirectWriteErr := os.WriteFile(attestationPath, []byte("worker-overwrite-attempt"), 0600)
	containmentPath := filepath.Join(job.TargetDir, ".ai-team", "state", "containment", job.RunID+".json")
	containmentData, containmentErr := os.ReadFile(containmentPath)
	containmentDirectWriteErr := os.WriteFile(containmentPath, []byte("worker-overwrite-attempt"), 0600)
	briefPath := filepath.Join(job.TargetDir, ".ai-team", "runs", job.RunID, "brief", "0001-intention.md")
	_, briefErr := os.ReadFile(briefPath)
	briefWriteErr := os.WriteFile(briefPath, []byte("worker-overwrite-attempt"), 0600)
	worktreeData, worktreeErr := os.ReadFile(filepath.Join(job.TargetDir, ".ai-team", "worktrees", "probe", "visible.txt"))
	targetData, targetErr := os.ReadFile(filepath.Join(job.TargetDir, "visible.txt"))
	writeErr := os.WriteFile(filepath.Join(job.TargetDir, "worker-write.txt"), []byte("worker-write"), 0600)
	agentWriteErr := os.WriteFile(filepath.Join(job.TargetDir, ".ai-team", "agents", "role.md"), []byte("modified"), 0600)
	apiReachable := false
	adminControlPlaneCallRejected := false
	usageAPIWriteSucceeded := false
	deliveryAPIWriteSucceeded := false
	attestationAPIWriteSucceeded := false
	containmentAPIWriteSucceeded := false
	candidateEvidenceAPIWriteReadSucceeded := false
	attemptManifestAPIWriteReadSucceeded := false
	briefAPIListReadSucceeded := false
	if port, portErr := NewWorkerAPIPort(job); portErr == nil {
		var approvals []approval.PendingApproval
		apiReachable = port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvals) == nil && len(approvals) == 0
		adminControlPlaneCallRejected = isExpectedAdminControlPlaneRejection(port.call("admin.control_plane", workerAPICall{RunID: job.RunID}, nil))
		started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
		usageEnvelope := metrics.Build(job.RunID, "probe", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
		usageAPIWriteSucceeded = port.call("usage.envelope.write", workerAPICall{Usage: usageEnvelope}, nil) == nil
		deliveryRecord := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: job.RunID, Feature: "probe", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
		deliveryAPIWriteSucceeded = NewWorkerAPITerminalRecordWriter(port).WriteTerminalRecord(deliveryRecord) == nil
		workerStatement := &attest.Statement{Type: attest.StatementType, PredicateType: attest.PredicateTypeV1, Predicate: attest.Predicate{SchemaVersion: attest.PredicateSchemaVersion, RunID: job.RunID}}
		attestationAPIWriteSucceeded = NewWorkerAPIAttestationWriter(port).WriteAttestation(workerStatement) == nil
		containmentAPIWriteSucceeded = NewWorkerAPIContainmentReceiptWriter(port).WriteContainmentReceipt(containment.DefaultTrustedLocalReceipt()) == nil
		candidateEvidenceStore := NewWorkerAPICandidateEvidenceStore(port)
		probeDocument := pipeline.CandidateEvidence{
			SchemaVersion: 1, RunID: job.RunID, Purpose: "semantic_code_review",
			WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: []pipeline.CandidateFile{},
			Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
		}
		if writeErr := candidateEvidenceStore.WriteCandidateEvidence("review-candidate.json", probeDocument); writeErr == nil {
			loaded, readErr := candidateEvidenceStore.ReadCandidateEvidence("review-candidate.json")
			candidateEvidenceAPIWriteReadSucceeded = readErr == nil && loaded.WorkspaceSHA256 == probeDocument.WorkspaceSHA256
		}
		attemptManifestStore := NewWorkerAPIAttemptManifestStore(port)
		manifestNow := time.Now().UTC()
		probeManifest := evidence.AttemptManifest{SchemaVersion: evidence.SchemaVersion, RunID: job.RunID, AttemptID: "api-probe", Stage: "probe", StageIndex: 1,
			StartedAt: manifestNow.Add(-time.Minute), FinishedAt: manifestNow, Status: "completed", Execution: "success", Decision: "continue", Outcome: "success"}
		if writeErr := attemptManifestStore.WriteAttemptManifest(probeManifest); writeErr == nil {
			loaded, readErr := attemptManifestStore.ReadAttemptManifest("", job.RunID, probeManifest.AttemptID)
			attemptManifestAPIWriteReadSucceeded = readErr == nil && len(loaded) > 0
		}
		briefs := NewWorkerAPIBriefs(port)
		_, createErr := briefs.CreateInitial(job.RunID, job.Task)
		versions, listErr := briefs.List(job.RunID)
		if listErr == nil && len(versions) == 1 {
			document, readErr := briefs.Read(job.RunID, versions[0].ID)
			briefAPIListReadSucceeded = createErr == nil && readErr == nil && string(document.Content) == "# Исходное намерение\n\ntest worker filesystem boundary\n"
		}
	}
	canDial := func(address string) bool {
		conn, dialErr := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	openAIProxyReachable, deniedOtherHost, deniedOtherPort := runOpenAIEgressProbe(t)
	report := sandboxProbeReport{
		DatabaseReadable:                       dbErr == nil && strings.Contains(string(dbData), "controller-db-secret"),
		WALReadable:                            walErr == nil && strings.Contains(string(walData), "controller-wal-secret"),
		SHMReadable:                            shmErr == nil && strings.Contains(string(shmData), "controller-shm-secret"),
		JournalReadable:                        journalErr == nil && strings.Contains(string(journalData), "controller-journal-secret"),
		LifecycleReadable:                      lifecycleErr == nil && strings.Contains(string(lifecycleData), "lifecycle-secret"),
		LegacyApprovalReadable:                 approvalErr == nil && strings.Contains(string(approvalData), "legacy-approval-secret"),
		CandidateMetadataReadable:              candidateErr == nil && strings.Contains(string(candidateData), "candidate-metadata-secret"),
		CandidateEvidenceReadable:              candidateEvidenceErr == nil && len(candidateEvidenceData) > 0,
		CandidateEvidenceDirectWriteSucceeded:  candidateEvidenceDirectWriteErr == nil,
		CandidateEvidenceAPIWriteReadSucceeded: candidateEvidenceAPIWriteReadSucceeded,
		AttemptManifestReadable:                attemptManifestErr == nil && len(attemptManifestData) > 0,
		AttemptManifestDirectWriteSucceeded:    attemptManifestDirectWriteErr == nil,
		AttemptManifestAPIWriteReadSucceeded:   attemptManifestAPIWriteReadSucceeded,
		UsageStateReadable:                     usageErr == nil && strings.Contains(string(usageData), "usage-envelope-secret"),
		DeliveryStateReadable:                  deliveryErr == nil && len(deliveryData) > 0,
		DeliveryDirectWriteSucceeded:           deliveryDirectWriteErr == nil,
		DeliveryAPIWriteSucceeded:              deliveryAPIWriteSucceeded,
		AttestationStateReadable:               attestationErr == nil && len(attestationData) > 0,
		AttestationDirectWriteSucceeded:        attestationDirectWriteErr == nil,
		AttestationAPIWriteSucceeded:           attestationAPIWriteSucceeded,
		ContainmentStateReadable:               containmentErr == nil && len(containmentData) > 0,
		ContainmentDirectWriteSucceeded:        containmentDirectWriteErr == nil,
		ContainmentAPIWriteSucceeded:           containmentAPIWriteSucceeded,
		UsageAPIWriteSucceeded:                 usageAPIWriteSucceeded,
		BriefSourceReadable:                    briefErr == nil,
		BriefSourceWriteSucceeded:              briefWriteErr == nil,
		BriefAPIListReadSucceeded:              briefAPIListReadSucceeded,
		WorktreeReadable:                       worktreeErr == nil && string(worktreeData) == "worktree-visible",
		TargetReadable:                         targetErr == nil && string(targetData) == "target-visible",
		TargetWritable:                         writeErr == nil,
		AgentRegistryWritable:                  agentWriteErr == nil,
		ControllerAPIReachable:                 apiReachable,
		AdminControlPlaneCallRejected:          adminControlPlaneCallRejected,
		AuthSecretAbsent:                       os.Getenv("AI_TEAM_AUTH_SECRET") == "",
		SigningKeyAbsent:                       os.Getenv("AI_TEAM_SIGNING_KEY") == "",
		DatabasePasswordAbsent:                 os.Getenv("AI_TEAM_DB_PASSWORD") == "",
		HostingWriteTokenAbsent:                os.Getenv("AI_TEAM_HOSTING_WRITE_TOKEN") == "",
		HostTCPReachable:                       canDial(value("--probe-host-tcp")),
		OutboundTCPReachable:                   canDial("1.1.1.1:443"),
		OpenAIProxyReachable:                   openAIProxyReachable,
		OpenAIDeniedOtherHost:                  deniedOtherHost,
		OpenAIDeniedOtherPort:                  deniedOtherPort,
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(value("--probe-output"), encoded, 0600); err != nil {
		t.Fatalf("write probe report: %v", err)
	}
	result, err := json.Marshal(Result{SchemaVersion: ResultSchemaVersion, RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, Outcome: OutcomeCompleted})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", ResultPrefix, result)
}

func isExpectedAdminControlPlaneRejection(err error) bool {
	var responseErr workerAPIResponseError
	return errors.As(err, &responseErr) &&
		responseErr.status == http.StatusBadRequest &&
		responseErr.message == `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`
}

func TestExpectedAdminControlPlaneRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "expected worker API denial",
			err:  workerAPIResponseError{status: http.StatusBadRequest, message: `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`},
			want: true,
		},
		{name: "transport error", err: errors.New("connection reset")},
		{name: "authentication error", err: workerAPIResponseError{status: http.StatusUnauthorized, message: "worker API unauthorized"}},
		{name: "wrong response status", err: workerAPIResponseError{status: http.StatusInternalServerError, message: `worker API admin.control_plane: worker API method "admin.control_plane" is not allowed`}},
		{name: "wrong rejection message", err: workerAPIResponseError{status: http.StatusBadRequest, message: "worker API admin.control_plane: invalid request"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExpectedAdminControlPlaneRejection(tt.err); got != tt.want {
				t.Fatalf("isExpectedAdminControlPlaneRejection(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func argsAfterDoubleDash(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}
