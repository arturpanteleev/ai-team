package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
)

func TestControllerEventSourceFeedsDeliveryAnchorAndAttestation(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	runID := "controller-events-delivery"
	controllerEvents := evidence.ControllerEventStore{TargetDir: target}
	if err := controllerEvents.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	store, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "event-authority", TargetDir: target, StartedAt: started,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	}, controllerEvents)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_started", Timestamp: started}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "delivery_deferred", AttemptID: "delivery-attempt", Timestamp: started.Add(time.Minute), Data: map[string]any{
		"plan_hash": strings.Repeat("a", 64), "feature": "event-authority", "state_path": filepath.ToSlash(filepath.Join(target, ".ai-team", "delivery", "event-authority.json")),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(evidence.Event{Type: "run_finished", Timestamp: started.Add(2 * time.Minute), Data: map[string]any{"status": "completed", "stage_attempts": 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.RunDir(), "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("cloud evidence unexpectedly published a local event log: %v", err)
	}
	marker, err := firstDeferredMarker(store.RunDir(), controllerEvents)
	if err != nil || marker.PlanHash != strings.Repeat("a", 64) {
		t.Fatalf("delivery marker=%+v err=%v", marker, err)
	}
	status, err := terminalStatusOfRun(store.RunDir(), runID, controllerEvents)
	if err != nil || status != "completed" {
		t.Fatalf("delivery terminal status=%q err=%v", status, err)
	}
	if err := evidence.VerifyAnchorWithEventSource(store.RunDir(), controllerEvents); err != nil {
		t.Fatalf("controller event anchor verification: %v", err)
	}
	statement, err := attest.Build(attest.Options{RunDir: store.RunDir(), RunID: runID, FinishedAt: started.Add(2 * time.Minute), Outcome: "completed", EventLogSource: controllerEvents})
	if err != nil {
		t.Fatalf("controller event attestation build: %v", err)
	}
	bytes, err := controllerEvents.ReadBytes(runID)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(bytes)
	if statement.Predicate.Run.EventLogSHA256 != fmt.Sprintf("%x", want[:]) {
		t.Fatalf("attestation digest=%s, canonical events=%x", statement.Predicate.Run.EventLogSHA256, want)
	}
}

// Regression A5-1 (AUD-05): delivery state после утверждённой delivery-стадии
// лежит в РЕАЛЬНОМ подготовленном расположении <target>/.ai-team/delivery/
// <feature>.json, и delivery_deferred event обязан зафиксировать именно это
// состояние (state_path + plan_hash). DeliverDeferred не выдумывает state из
// аргументов вызывающего: чужой target обязан дать fail-closed, а доставка
// идёт ровно по реальному prepared state (plan.hash совпадает с маркером).
func TestDeferredDeliveryResolvesRealPreparedState(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err == nil {
		t.Fatal("post-terminal hook сбоит — Run обязан вернуть ошибку")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)

	// RunWithResult канонизирует TargetDir (pipeline.go: filepath.EvalSymlinks —
	// macOS /var → /private/var); именно этот каноничный корень фиксируется в
	// state_path события. Ожидаем реальное расположение от него же.
	canonicalDir, symlinkErr := filepath.EvalSymlinks(dir)
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	realState := filepath.Join(canonicalDir, ".ai-team", "delivery", "feat.json")

	events, evErr := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), runID)
	if evErr != nil {
		t.Fatalf("event chain: %v", evErr)
	}
	foundMarker := false
	for _, event := range events {
		if event.Type != "delivery_deferred" {
			continue
		}
		foundMarker = true
		statePath, _ := event.Data["state_path"].(string)
		if statePath != filepath.ToSlash(realState) {
			t.Fatalf("state_path=%q, ожидали реальное расположение %q", statePath, filepath.ToSlash(realState))
		}
		planHash, _ := event.Data["plan_hash"].(string)
		if planHash != approvedPlanHash {
			t.Fatalf("plan_hash=%q, ожидали утверждённый %q", planHash, approvedPlanHash)
		}
	}
	if !foundMarker {
		t.Fatal("delivery_deferred event не записан")
	}
	if _, statErr := os.Stat(realState); statErr != nil {
		t.Fatalf("prepared state не записан в реальное расположение: %v", statErr)
	}

	plan, found, loadErr := delivery.LoadPreparedPlan(dir, "feat")
	if loadErr != nil || !found {
		t.Fatalf("LoadPreparedPlan(dir): found=%v err=%v", found, loadErr)
	}
	planHash, hashErr := plan.Hash()
	if hashErr != nil || planHash != approvedPlanHash {
		t.Fatalf("prepared plan hash=%q, ожидали %q (err=%v)", planHash, approvedPlanHash, hashErr)
	}
	if len(plan.Files) != 1 || plan.Files[0] != "change.go" {
		t.Fatalf("prepared plan files=%v", plan.Files)
	}
	digest, digestErr := checks.WorkspaceDigest(dir)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	if plan.VerifiedWorkspaceDigest != digest {
		t.Fatalf("prepared workspace digest %q != реальный %q", plan.VerifiedWorkspaceDigest, digest)
	}
	if verifyErr := delivery.VerifyPreparedWorkspace(dir, plan, digest); verifyErr != nil {
		t.Fatalf("VerifyPreparedWorkspace: %v", verifyErr)
	}

	// Чужой target не выдумывает state: подготовленное state берётся только из
	// реального расположения (или явный fail-closed). После AUD-05 (PR #96)
	// workspace резолвится из state_path маркера и до попадания в
	// "prepared plan отсутствует" уже отклоняется как чужой: "вне control
	// target". Оба — fail-closed одного смысла.
	wrongTarget := t.TempDir()
	if _, err := New(nil, nil).DeliverDeferred(context.Background(), runDir, "", wrongTarget); err == nil || !(strings.Contains(err.Error(), "prepared plan отсутствует") || strings.Contains(err.Error(), "вне control target")) {
		t.Fatalf("чужим target должен быть fail-closed, got: %v", err)
	}

	record, err := New(nil, nil, WithDeliveryService(&fakeDeliveryService{})).DeliverDeferred(context.Background(), runDir, "", dir)
	if err != nil {
		t.Fatalf("DeliverDeferred по реальному state: %v", err)
	}
	if record.PlanHash != approvedPlanHash || record.Feature != "feat" || record.CommitSHA == "" {
		t.Fatalf("terminal record не согласован с реальным state: %+v", record)
	}
}

func TestReconcileTerminalDeliveryRejectsValidRecordWithWrongPlanIdentity(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err == nil {
		t.Fatal("post-terminal hook failure should leave a deferred delivery obligation")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)

	// First write a valid delivery record for this run. Change only the plan hash,
	// then rewrite it through the canonical record writer so this remains a
	// structurally valid, self-checksummed record copied from a different plan.
	if _, err := New(nil, nil, WithDeliveryService(&fakeDeliveryService{})).DeliverDeferred(context.Background(), runDir, "", dir); err != nil {
		t.Fatalf("write initial terminal record: %v", err)
	}
	record, found, err := delivery.ReadTerminalRecord(runDir)
	if err != nil || !found {
		t.Fatalf("read initial terminal record: found=%v err=%v", found, err)
	}
	if err := delivery.WriteControllerTerminalRecord(dir, runID, *record); err != nil {
		t.Fatalf("migrate test record to controller store: %v", err)
	}
	if err := os.Remove(filepath.Join(runDir, "delivery.json")); err != nil {
		t.Fatal(err)
	}
	if err := New(nil, nil).ReconcileTerminalDelivery(context.Background(), runID, dir); err != nil {
		t.Fatalf("reconcile should accept a valid controller-owned record: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".ai-team", "state", "delivery", runID+".json")); err != nil {
		t.Fatal(err)
	}
	record.PlanHash = strings.Repeat("c", 64)
	record.RecordSHA256 = ""
	if err := delivery.WriteControllerTerminalRecord(dir, runID, *record); err != nil {
		t.Fatalf("write valid mismatched terminal record: %v", err)
	}
	if _, found, err := delivery.ReadControllerTerminalRecord(dir, runID); err != nil || !found {
		t.Fatalf("fixture must remain a valid delivery record: found=%v err=%v", found, err)
	}

	err = New(nil, nil).ReconcileTerminalDelivery(context.Background(), runID, dir)
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("reconcile must reject valid record with a plan hash from another delivery, got: %v", err)
	}
}

// lockProbeDeliveryService проверяет, что в момент controller.Execute run всё
// ещё держит workspace lock: повторный захват обязан конфликтовать. Если lock
// свободен — delivery-проба возвращает ошибку (доставка вне lock запрещена).
type lockProbeDeliveryService struct {
	dir      string
	lockHeld bool
}

func (f *lockProbeDeliveryService) Execute(_ context.Context, request delivery.Request) (delivery.Result, error) {
	if lock, err := evidence.AcquireWorkspaceLock(f.dir); err == nil {
		_ = lock.Close()
		return delivery.Result{}, errors.New("lock probe: доставка выполнена без занятого workspace lock")
	}
	f.lockHeld = true
	hash, _ := request.Plan.Hash()
	return delivery.Result{PlanHash: hash, CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PRURL: "https://example.test/pr/2"}, nil
}

// Regression A5-2 (AUD-05): deferred delivery выполняется, пока run всё ещё
// держит workspace lock (engine.AcquireWorkspaceLock) — конкурентный повторный
// захват внутри controller.Execute обязан конфликтовать.
func TestDeferredDeliveryRunsUnderWorkspaceLock(t *testing.T) {
	dir := env(t)
	approvedPlanHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	service := &lockProbeDeliveryService{dir: dir}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(service))
	if err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: approvedPlanHash,
	}); err != nil {
		t.Fatalf("run с доставкой под workspace lock должен пройти: %v", err)
	}
	if !service.lockHeld {
		t.Fatal("доставка выполнена без занятого workspace lock run'а")
	}
	runDir := onlyRunDir(t, dir)
	if _, ok, err := delivery.ReadTerminalRecord(runDir); err != nil || !ok {
		t.Fatalf("terminal record после доставки: ok=%v err=%v", ok, err)
	}
}

func TestManualDeliveryRejectsUnverifiedLegacyCloudClaims(t *testing.T) {
	target := t.TempDir()
	runID := "legacy-cloud-delivery-forged"
	runDir := writeLegacyControllerDeliveryEvidence(t, target, runID, "hash_flag", "attempt-delivery", strings.Repeat("a", 64))
	if err := (metrics.FileUsageEnvelopeStore{}).Reserve(target, runID); err != nil {
		t.Fatal(err)
	}
	service := &fakeDeliveryService{}
	p := New(nil, nil, WithDeliveryService(service))
	if _, err := p.DeliverDeferred(context.Background(), runDir, "cloud-feature", target); err == nil {
		t.Fatal("manual delivery accepted worker-supplied hash_flag approval without controller authority")
	}
	if service.calls != 0 {
		t.Fatalf("forged legacy chain initiated delivery %d times", service.calls)
	}
}

func TestLegacyDeliveryClassificationUsesControllerCandidateAdmission(t *testing.T) {
	target := t.TempDir()
	runID := "legacy-cloud-candidate-admission"
	runDir := writeLegacyControllerDeliveryEvidence(t, target, runID, "hash_flag", "attempt-delivery", strings.Repeat("e", 64))
	if err := (candidate.FileMetadataStore{}).MarkGitAdmission(target, runID); err != nil {
		t.Fatal(err)
	}
	service := &fakeDeliveryService{}
	p := New(nil, nil, WithDeliveryService(service))
	if _, err := p.DeliverDeferred(context.Background(), runDir, "cloud-feature", target); err == nil {
		t.Fatal("manual delivery accepted a hash_flag claim despite controller Git-admission proof")
	}
	if service.calls != 0 {
		t.Fatalf("forged legacy chain initiated delivery %d times", service.calls)
	}
}

func TestManualDeliveryUsesFinalCancellationStatus(t *testing.T) {
	target := t.TempDir()
	runID := "delivery-canceled-after-finish"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	store, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "cancelled-feature", TargetDir: target, StartedAt: started,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []evidence.Event{
		{Type: "run_started", Timestamp: started},
		{Type: "run_finished", Timestamp: started.Add(time.Second), Data: map[string]any{"status": "completed"}},
		{Type: "run_canceled", Timestamp: started.Add(2 * time.Second), Data: map[string]any{"status": "canceled"}},
		{Type: "run_finished", Timestamp: started.Add(3 * time.Second), Data: map[string]any{"status": "canceled"}},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	status, err := terminalStatusOfRun(store.RunDir(), runID, nil)
	if err != nil || status != "canceled" {
		t.Fatalf("manual delivery terminal status=%q err=%v; cancellation must override earlier completion", status, err)
	}
}

func TestLegacyCloudDeliveryRequiresExactControllerApproval(t *testing.T) {
	for _, test := range []struct {
		name       string
		approvalID string
		attemptID  string
		wantError  bool
	}{
		{name: "different attempt", approvalID: "approval-wrong-attempt", attemptID: "other-attempt", wantError: true},
		{name: "exact approval", approvalID: "approval-exact-attempt", attemptID: "attempt-delivery"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			runID := "legacy-cloud-" + strings.ReplaceAll(test.name, " ", "-")
			planHash := strings.Repeat("b", 64)
			runDir := writeLegacyControllerDeliveryEvidence(t, target, runID, "resolved_approval", "attempt-delivery", planHash)
			if err := (metrics.FileUsageEnvelopeStore{}).Reserve(target, runID); err != nil {
				t.Fatal(err)
			}
			approvals, err := approval.NewStore(target)
			if err != nil {
				t.Fatal(err)
			}
			value, err := approvals.Create(approval.PendingApproval{
				RunID: runID, ID: test.approvalID, AttemptID: test.attemptID, FromStage: "deployer", ToStage: "deployer",
				Trigger: "delivery_plan", SubjectHash: planHash, RequiredRoles: []string{"release_manager"},
				Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "deployer", "reject": "deployer"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := approvals.Decide(runID, value.ID, approval.Decision{
				ActorID: "release-manager", ActorRole: "release_manager", Action: "approve", SubjectHash: planHash,
			}); err != nil {
				t.Fatal(err)
			}
			p := New(nil, nil, WithApprovalStore(approvals))
			err = p.validateControllerBackedLegacyDelivery(target, runDir, runID)
			if (err != nil) != test.wantError {
				t.Fatalf("legacy delivery authority error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestReservedAndLocalDeliverySourcesKeepTheirExistingAuthority(t *testing.T) {
	t.Run("reserved controller event log", func(t *testing.T) {
		target := t.TempDir()
		runID := "reserved-delivery-authority"
		if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
			t.Fatal(err)
		}
		manifest := evidence.RunManifest{RunID: runID, Feature: "cloud-feature", TargetDir: target, StartedAt: time.Now().UTC(),
			ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`)}
		eventStore := evidence.ControllerEventStore{TargetDir: target}
		if err := eventStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		run, err := evidence.StartWithEventLog(filepath.Join(target, ".ai-team", "runs"), manifest, eventStore)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now().UTC()
		for _, event := range []evidence.Event{
			{Type: "run_started", Timestamp: started},
			{Type: "attempt_started", Stage: "deployer", AttemptID: "attempt-delivery", Timestamp: started.Add(time.Second)},
			{Type: "delivery_plan_approved", AttemptID: "attempt-delivery", Timestamp: started.Add(2 * time.Second), Data: map[string]any{"plan_hash": strings.Repeat("c", 64), "mode": "hash_flag"}},
			{Type: "delivery_deferred", AttemptID: "attempt-delivery", Timestamp: started.Add(3 * time.Second), Data: map[string]any{
				"plan_hash": strings.Repeat("c", 64), "feature": "cloud-feature", "state_path": filepath.ToSlash(filepath.Join(target, ".ai-team", "delivery", "cloud-feature.json")),
			}},
		} {
			if err := run.Append(event); err != nil {
				t.Fatal(err)
			}
		}
		p := New(nil, nil)
		if err := p.validateControllerBackedLegacyDelivery(target, run.RunDir(), runID); err != nil {
			t.Fatalf("reserved canonical event log incorrectly used legacy fallback: %v", err)
		}
		if marker, err := firstDeferredMarker(run.RunDir(), nil); err != nil || marker.PlanHash != strings.Repeat("c", 64) {
			t.Fatalf("reserved canonical event marker=%+v err=%v", marker, err)
		}
	})

	t.Run("local CLI file journal", func(t *testing.T) {
		target := t.TempDir()
		runID := "local-delivery-authority"
		runDir := writeLegacyControllerDeliveryEvidence(t, target, runID, "hash_flag", "attempt-delivery", strings.Repeat("d", 64))
		if err := (New(nil, nil)).validateControllerBackedLegacyDelivery(target, runDir, runID); err != nil {
			t.Fatalf("local file-backed delivery was treated as controller-backed: %v", err)
		}
	})
}

func writeLegacyControllerDeliveryEvidence(t *testing.T, target, runID, mode, attemptID, planHash string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	store, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "cloud-feature", TargetDir: target, StartedAt: started,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []evidence.Event{
		{Type: "run_started", Timestamp: started},
		{Type: "attempt_started", Stage: "deployer", AttemptID: attemptID, Timestamp: started.Add(time.Second)},
		{Type: "delivery_plan_approved", AttemptID: attemptID, Timestamp: started.Add(2 * time.Second), Data: map[string]any{"plan_hash": planHash, "mode": mode}},
		{Type: "delivery_deferred", AttemptID: attemptID, Timestamp: started.Add(3 * time.Second), Data: map[string]any{
			"plan_hash": planHash, "feature": "cloud-feature", "state_path": filepath.ToSlash(filepath.Join(target, ".ai-team", "delivery", "cloud-feature.json")),
		}},
		{Type: "run_finished", Timestamp: started.Add(4 * time.Second), Data: map[string]any{"status": "completed"}},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	return store.RunDir()
}
