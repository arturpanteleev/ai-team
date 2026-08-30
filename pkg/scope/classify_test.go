package scope

import "testing"

func TestClassifyMutation(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// tests
		{"pkg/foo/foo_test.go", ClassTests},
		{"pkg/foo/something_test.go", ClassTests},
		{"testdata/golden.json", ClassTests},
		{"e2e/pipeline_test.go", ClassTests},
		{"e2etest/suite/fixture.txt", ClassTests},
		{"web/src/component.test.tsx", ClassTests},
		{"web/src/component.spec.js", ClassTests},
		{"tests/integration/flow.ts", ClassTests},
		{"__tests__/math/add_test.ts", ClassTests},
		{"src/pkg/unit_test/main.go", ClassTests},
		{"mypkg_unit_test/main.go", ClassTests}, // *_test каталог
		// generated
		{"generated/api/api.pb.go", ClassGenerated},
		{"src/tools/gen/parser.go", ClassGenerated},
		{"web/dist/app.min.js", ClassGenerated},
		{"web/dist/app.js.map", ClassGenerated},
		{"vendor/golang.org/x/tools/internal/p.go", ClassGenerated},
		{"coverage/coverage.json", ClassGenerated},
		// generated не перекрывает tests
		{"generated/api/api_test.go", ClassTests},
		// infra
		{".github/workflows/ci.yml", ClassInfra},
		{".gitlab-ci.yml", ClassInfra},
		{"infra/terraform/main.tf", ClassInfra},
		{"k8s/deploy.yaml", ClassInfra},
		{"Dockerfile", ClassInfra},
		{"deploy/monitoring/Dockerfile.web", ClassInfra},
		{"Makefile", ClassInfra},
		{"scripts/build.mk", ClassInfra},
		// meta
		{"go.mod", ClassMeta},
		{"go.sum", ClassMeta},
		{"package.json", ClassMeta},
		{"package-lock.json", ClassMeta},
		{"sub/dir/pyproject.toml", ClassMeta},
		{".gitignore", ClassMeta},
		{".golangci.yml", ClassMeta},
		// source
		{"pkg/checks/checks.go", ClassSource},
		{"cmd/main.go", ClassSource},
		{"web/src/App.tsx", ClassSource},
		{"scripts/deploy.py", ClassSource},
		{"Makefile_targets.c", ClassSource},
		// unknown
		{"README.md", ClassUnknown},
		{"docs/ARCHITECTURE.md", ClassUnknown},
		{"openspec/feature/specs/a.md", ClassUnknown},
		{"agent/icon.png", ClassUnknown},
		{"LICENSE", ClassUnknown},
		{"data/report.xlsx", ClassUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := ClassifyMutation(tc.path); got != tc.want {
				t.Fatalf("ClassifyMutation(%q)=%q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
