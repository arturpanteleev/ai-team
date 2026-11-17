package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// newForbidSelfApprovalFixture pins a template with forbid_self_approval: true
// so a direct controller call exercises the policy without a web session.
func newForbidSelfApprovalFixture(t *testing.T, state lifecycle.State, engine *fakeEngine, store *recordingApprovalStore) (*Controller, string) {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ForbidSelfApproval = true
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	templateStore, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinCurrentForRun(state.RunID, state.RunID); err != nil {
		t.Fatal(err)
	}
	state.TargetDir = target
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(state); err != nil {
		t.Fatal(err)
	}
	controller, err := New(engine, target, WithApprovalStore(store))
	if err != nil {
		t.Fatal(err)
	}
	return controller, target
}

func returnApproval(runID, approvalID string) approval.PendingApproval {
	return approval.PendingApproval{
		ID: approvalID, RunID: runID, AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "code_review", ToStage: "code_review", Trigger: "graph_outcome:passed",
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"reviewer"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "return_to_implementation"},
		Targets: map[string]string{"approve": "implementation", "return_to_implementation": "implementation"},
	}
}

// A return is a judgment on the confirmed stage visit. A direct, non-HTTP call
// must not be able to send the stage back to its author, and an unavailable
// authorship check must fail closed rather than silently allow the return.
func TestControllerReturnStageEnforcesSelfApprovalOnDirectCall(t *testing.T) {
	const author = "author@example.test"
	const reviewer = "reviewer@example.test"

	for _, tc := range []struct {
		name       string
		actor      string
		install    bool
		forbid     bool
		wantRefuse bool
	}{
		{"author is refused", author, true, true, true},
		{"another reviewer passes", reviewer, true, true, false},
		{"unavailable check fails closed", reviewer, false, true, true},
		{"policy off needs no check", author, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
			store := newRecordingApprovalStore(returnApproval("run-return", "approval-return"))
			state := stageActionState("run-return", "code_review", "approval-return", lifecycle.PhaseWaiting)
			newFixture := newForbidSelfApprovalFixture
			if !tc.forbid {
				newFixture = newStageActionFixture
			}
			controller, _ := newFixture(t, state, engine, store)
			if tc.install {
				check := func(_ string, _ approval.PendingApproval, actorID string) error {
					return cloudidentity.CheckSelfApproval(true, actorID, []string{author})
				}
				controller.SetSelfApprovalChecker(check)
			}

			err := controller.ReturnStage(context.Background(), "run-return", "code_review", "implementation", "redo", tc.actor, "reviewer")
			if tc.wantRefuse {
				if !errors.Is(err, humandecision.ErrSelfApproval) {
					t.Fatalf("return must be refused as self-approval: %v", err)
				}
				if !strings.Contains(err.Error(), "return_to_implementation") || !strings.Contains(err.Error(), "implementation") {
					t.Fatalf("refusal must name the return route: %v", err)
				}
				if len(store.decisions) != 0 {
					t.Fatalf("a refused return must not spend the decision: %+v", store.decisions)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store.decisions) != 1 || store.decisions[0].Action != "return_to_implementation" ||
				store.decisions[0].ActorID != tc.actor || store.decisions[0].ActorRole != "reviewer" {
				t.Fatalf("return decision mismatch: %+v", store.decisions)
			}
			<-engine.started
			close(engine.release)
			waitControllerIdle(t, controller, "run-return")
		})
	}
}

// The system-admin gate override is a separate authority: the self-approval
// check must not leak into it even when the same person is a stage author.
func TestControllerSystemAdminOverrideIgnoresReturnSelfApproval(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(checkDecisionApproval("run-override", "approval-override", "code_review", "reviewer"))
	controller, _ := newForbidSelfApprovalFixture(t,
		stageActionState("run-override", "code_review", "approval-override", lifecycle.PhaseWaiting), engine, store)
	// This checker would refuse every return by the administrator.
	controller.SetSelfApprovalChecker(func(string, approval.PendingApproval, string) error {
		return errors.New("actor worked on the stage")
	})

	if err := controller.OverrideStageCheck(context.Background(), "run-override", "code_review", "риск принят", "admin-1", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != "override" ||
		store.decisions[0].Authority != approval.AuthoritySystemAdmin {
		t.Fatalf("admin override must keep its authority: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-override")
}
