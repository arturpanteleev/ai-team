package web

import "testing"

func TestAllowedRunArtifactPathExposesStageCheckReport(t *testing.T) {
	for _, path := range []string{
		"checks/product_spec-round-01.md",
		"brief/0001-intention.md",
		"attempts/a/artifacts/feature/proposal.md",
	} {
		if !allowedRunArtifactPath(path) {
			t.Errorf("expected artifact path %q to be exposed", path)
		}
	}
	if allowedRunArtifactRevisionPath("checks/product_spec-round-01.md") {
		t.Error("check report must be read-only")
	}
	if allowedRunArtifactRevisionPath("./checks/product_spec-round-01.md") {
		t.Error("check report must remain read-only through a normalized path")
	}
	for _, path := range []string{
		"checks/private.json",
		"checks/nested/report.md",
		"feature/.env",
	} {
		if allowedRunArtifactPath(path) {
			t.Errorf("unexpected artifact path %q exposed", path)
		}
	}
}
