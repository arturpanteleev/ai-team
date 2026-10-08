package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

func TestRequireResolvedDeliveryApprovalBindsPlanAndAttempt(t *testing.T) {
	planTarget := env(t)
	wantHash := prepareDelivery(t, planTarget)
	plan, found, err := delivery.LoadPreparedPlan(planTarget, "feat")
	if err != nil || !found {
		t.Fatalf("load canonical prepared plan: found=%v err=%v", found, err)
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	duplicateKeyPayload := bytes.Replace(canonical,
		[]byte(`"commit_message": "feat change",`),
		[]byte("\"commit_message\": \"misleading payload\",\n  \"commit_message\": \"feat change\","), 1)
	if bytes.Equal(duplicateKeyPayload, canonical) {
		t.Fatal("duplicate-key approval payload fixture was not constructed")
	}
	const runID, stage, attemptID = "approval-binding-run", "delivery", "attempt-delivery"

	tests := []struct {
		name       string
		attemptID  string
		payload    []byte
		mutateHash bool
		resolve    bool
		create     bool
		wantErr    bool
	}{
		{name: "exact resolved approval", attemptID: attemptID, payload: canonical, resolve: true, create: true},
		{name: "misleading payload", attemptID: attemptID, payload: []byte(`{"message":"approve another plan"}`), resolve: true, create: true, wantErr: true},
		{name: "duplicate-key misleading payload", attemptID: attemptID, payload: duplicateKeyPayload, resolve: true, create: true, wantErr: true},
		{name: "empty payload", attemptID: attemptID, resolve: true, create: true, wantErr: true},
		{name: "foreign attempt", attemptID: "attempt-from-another-stage", payload: canonical, resolve: true, create: true, wantErr: true},
		{name: "unresolved approval", attemptID: attemptID, payload: canonical, create: true, wantErr: true},
		{name: "missing approval", wantErr: true},
		{name: "subject hash does not match plan", attemptID: attemptID, payload: canonical, mutateHash: true, resolve: true, create: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			store, err := approval.NewStore(target)
			if err != nil {
				t.Fatal(err)
			}
			if test.create {
				value, err := store.Create(approval.PendingApproval{
					RunID: runID, AttemptID: test.attemptID,
					FromStage: stage, ToStage: stage, Trigger: "delivery_plan",
					SubjectHash: wantHash, Payload: append(json.RawMessage(nil), test.payload...),
					RequiredRoles: []string{deliveryApprovalRole}, Quorum: approval.QuorumAny,
					Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": stage, "reject": stage},
				})
				if err != nil {
					t.Fatal(err)
				}
				if test.resolve {
					value, err = store.Decide(runID, value.ID, approval.Decision{
						ActorID: "release-manager-1", ActorRole: deliveryApprovalRole,
						Action: "approve", SubjectHash: wantHash,
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				if test.mutateHash {
					forgedHash := strings.Repeat("0", 64)
					value.SubjectHash = forgedHash
					value.Decisions[0].SubjectHash = forgedHash
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(target, ".ai-team", "state", "approvals", runID, value.ID+".json")
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}

			err = requireResolvedDeliveryApproval(store, runID, stage, attemptID, wantHash, canonical, "")
			if test.wantErr && err == nil {
				t.Fatal("mismatched or missing approval was accepted")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("exact approval rejected: %v", err)
			}
		})
	}
}

func TestRequireResolvedDeliveryOperationApprovalReusesOnlySavedDecisionForSamePlan(t *testing.T) {
	target := env(t)
	planHash := prepareDelivery(t, target)
	plan, found, err := delivery.LoadPreparedPlan(target, "feat")
	if err != nil || !found {
		t.Fatalf("load prepared plan: found=%v err=%v", found, err)
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	const runID, stage, approvedAttempt, replayAttempt = "operation-retry-run", "delivery", "attempt-original", "attempt-replay"
	controllerAttempts := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := controllerAttempts.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, attemptID := range []string{approvedAttempt, replayAttempt} {
		now := time.Now().UTC()
		if err := controllerAttempts.Write(runID, evidence.AttemptManifest{
			SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID, Stage: stage, StageIndex: 1,
			StartedAt: now.Add(-time.Minute), FinishedAt: now, Status: "failed", Execution: "failed", Decision: "continue", Outcome: "failed",
		}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(approval.PendingApproval{
		RunID: runID, AttemptID: approvedAttempt, FromStage: stage, ToStage: stage, Trigger: "delivery_plan",
		SubjectHash: planHash, RequiredRoles: []string{deliveryApprovalRole}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": stage, "reject": stage}, Payload: canonical,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(runID, value.ID, approval.Decision{
		ActorID: "release-manager-1", ActorRole: deliveryApprovalRole, Action: "approve", SubjectHash: planHash,
	}); err != nil {
		t.Fatal(err)
	}
	source := evidence.ReservedAttemptManifestSource{TargetDir: target}
	if err := requireResolvedDeliveryOperationApproval(store, source, runDir, runID, stage, planHash, canonical, ""); err != nil {
		t.Fatalf("same approved plan should survive delivery-stage retry: %v", err)
	}

	wrongPlan := append([]byte(nil), canonical...)
	wrongPlan = bytes.Replace(wrongPlan, []byte(`"commit_message": "feat change"`), []byte(`"commit_message": "different change"`), 1)
	if bytes.Equal(wrongPlan, canonical) {
		t.Fatal("failed to construct mismatched delivery plan")
	}
	wrongPlanHash := strings.Repeat("0", 64)
	if err := requireResolvedDeliveryOperationApproval(store, source, runDir, runID, stage, wrongPlanHash, wrongPlan, ""); err == nil {
		t.Fatal("saved approval for a different plan must not authorize a retry")
	}
	if err := requireResolvedDeliveryOperationApproval(store, source, runDir, runID, stage, strings.Repeat("1", 64), canonical, ""); err == nil {
		t.Fatal("saved approval with a different subject hash must not authorize a retry")
	}
	if err := requireResolvedDeliveryOperationApproval(store, source, runDir, "another-run", stage, planHash, canonical, ""); err == nil {
		t.Fatal("saved approval for another run must not authorize a retry")
	}
}

func TestReconcileTerminalDeliveryRequiresControllerApprovalAndAttempt(t *testing.T) {
	dir := env(t)
	planHash := prepareDelivery(t, dir)
	rt := newScripted()
	rt.content["approver"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	p := New(cfgFor(config.AgentConfig{Name: "approver"}, config.AgentConfig{Name: "deployer"}), deliveryRegistry(),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithDeliveryService(&gracefulDeliveryService{}))
	if err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true, ApprovePlanHash: planHash}); err == nil {
		t.Fatal("post-terminal hook failure should leave a deferred delivery obligation")
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	marker, err := firstDeferredMarker(runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if marker.Stage == "" || marker.AttemptID == "" {
		t.Fatalf("delivery marker lost event attempt identity: %+v", marker)
	}

	controllerAttempts := copyRunAttemptManifestsToControllerStore(t, dir, runDir, runID)
	approvalStore, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	eventSource := evidence.ControllerEventStore{TargetDir: dir}
	if err := eventSource.MigrateLegacy(runID, runDir); err != nil {
		t.Fatal(err)
	}
	controllerPipeline := New(nil, nil, WithApprovalStore(approvalStore),
		WithEventLogSource(eventSource), WithDeliveryService(&fakeDeliveryService{}))
	if err := controllerPipeline.ReconcileTerminalDelivery(context.Background(), runID, dir); err != nil {
		t.Fatalf("reconcile from matching controller approvals and attempt should pass: %v", err)
	}

	otherTarget := t.TempDir()
	otherApprovalStore, err := approval.NewStore(otherTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(nil, nil, WithApprovalStore(otherApprovalStore), WithEventLogSource(eventSource)).ReconcileTerminalDelivery(context.Background(), runID, dir); err == nil || !strings.Contains(err.Error(), "no resolved release-manager approval") {
		t.Fatalf("recovery without the controller-resolved approval should fail closed: %v", err)
	}

	manifestPath := filepath.Join(dir, ".ai-team", "state", "attempt-manifests", runID, marker.AttemptID+".json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var forged evidence.AttemptManifest
	if err := json.Unmarshal(manifestData, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Stage = "another-stage"
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := controllerAttempts.Write(runID, forged); err != nil {
		t.Fatal(err)
	}
	if err := controllerPipeline.ReconcileTerminalDelivery(context.Background(), runID, dir); err == nil || !strings.Contains(err.Error(), "marker stage") {
		t.Fatalf("recovery accepted a controller attempt from a different stage: %v", err)
	}
}
