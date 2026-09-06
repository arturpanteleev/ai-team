package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

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
	// реального расположения (или явный fail-closed).
	wrongTarget := t.TempDir()
	if _, err := New(nil, nil).DeliverDeferred(runDir, "", wrongTarget); err == nil || !strings.Contains(err.Error(), "prepared plan отсутствует") {
		t.Fatalf("чужим target должен быть fail-closed, got: %v", err)
	}

	record, err := New(nil, nil, WithDeliveryService(&fakeDeliveryService{})).DeliverDeferred(runDir, "", dir)
	if err != nil {
		t.Fatalf("DeliverDeferred по реальному state: %v", err)
	}
	if record.PlanHash != approvedPlanHash || record.Feature != "feat" || record.CommitSHA == "" {
		t.Fatalf("terminal record не согласован с реальным state: %+v", record)
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
