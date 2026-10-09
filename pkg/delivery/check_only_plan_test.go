package delivery

import "testing"

func TestPlanAllowsControllerChecksWithoutArtifactVerdicts(t *testing.T) {
	plan := validTestPlan()
	plan.Preconditions = nil
	if err := plan.Validate(); err != nil {
		t.Fatalf("a controller-check-only delivery plan should be valid: %v", err)
	}
	if _, err := plan.Hash(); err != nil {
		t.Fatalf("check-only plan should have a canonical hash: %v", err)
	}
}
