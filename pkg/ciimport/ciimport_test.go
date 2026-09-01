package ciimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

func writeWorkflow(t *testing.T, target, name, content string) {
	t.Helper()
	dir := filepath.Join(target, filepath.FromSlash(WorkflowPath))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImportGitHubActionsBasic(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
name: CI
on: [push]
jobs:
  lint:
    steps:
      - uses: actions/checkout@v4
      - run: go vet ./...
  test:
    steps:
      - run: go test ./... -race
  build:
    steps:
      - run: go build ./...
  unknown:
    steps:
      - run: make deploy
`)
	imp, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imp.WorkflowCount != 1 {
		t.Errorf("workflow count = %d, want 1", imp.WorkflowCount)
	}
	names := []string{}
	for _, d := range imp.Definitions {
		names = append(names, d.Name)
	}
	for _, want := range []string{"go-vet", "go-test", "go-build"} {
		found := false
		for _, n := range names {
			if strings.HasPrefix(n, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ожидали check %q, got %v", want, names)
		}
	}
	// race option stays in the command for go test -race
	var raceInCommand bool
	for _, d := range imp.Definitions {
		if err := d.Validate(); err != nil {
			t.Errorf("imported definition %q invalid: %v", d.Name, err)
		}
		for _, a := range d.Command {
			if a == "-race" {
				raceInCommand = true
			}
		}
	}
	if !raceInCommand {
		t.Error("ожидали -race в command для go test -race")
	}
	// неизвестные шаги пропущены, не исполняются
	var skippedUnknown bool
	for _, s := range imp.Skipped {
		if s.Command == "make deploy" {
			skippedUnknown = true
			break
		}
	}
	if !skippedUnknown {
		t.Error("неизвестный run-шаг должен попасть в Skipped")
	}
	if len(imp.Skipped) < 2 { // checkout (uses) + unknown run
		t.Errorf("skipped = %d, want >= 2", len(imp.Skipped))
	}
	if imp.Fingerprint == "" {
		t.Fatal("fingerprint пуст")
	}
}

func TestImportDedupeAndDeterministicFingerprint(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "a.yml", `
jobs:
  test:
    steps:
      - run: go test ./...
`)
	impA, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(impA.Definitions) != 1 || impA.Definitions[0].Name != "go-test" {
		t.Fatalf("unexpected suite: %+v", impA.Definitions)
	}
	// повторный import того же дерева даёт тот же fingerprint (детерминизм)
	impA2, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatal(err)
	}
	if impA2.Fingerprint != impA.Fingerprint {
		t.Errorf("fingerprint не детерминирован: %s != %s", impA.Fingerprint, impA2.Fingerprint)
	}
	// второй workflow с таким же go test должен дедуплицироваться после import
	writeWorkflow(t, dir, "b.yml", `
jobs:
  test:
    steps:
      - run: go test ./...
`)
	impB, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(impB.Definitions) != 1 {
		t.Errorf("dedupe failed: %d", len(impB.Definitions))
	}
}

func TestImportRejectsShellOperators(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ops.yml", `
jobs:
  test:
    steps:
      - run: go test ./... && go vet ./...
`)
	imp, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Definitions) != 0 {
		t.Errorf("multicmd run не должен импортироваться, got %+v", imp.Definitions)
	}
	if len(imp.Skipped) == 0 {
		t.Error("multicmd run должен быть в Skipped")
	}
}

func TestImportNoWorkflows(t *testing.T) {
	dir := t.TempDir()
	imp, err := Import(dir, FormatGitHubActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Definitions) != 0 || imp.WorkflowCount != 0 {
		t.Errorf("нет workflow: ожидали пустой импорт")
	}
}

func TestMapUsesGolangci(t *testing.T) {
	d, ok := mapStep(step{Uses: "golangci/golangci-lint-action@v6"})
	if !ok {
		t.Fatal("golangci-lint action должен быть отображён")
	}
	if d.Name != "golangci-lint" || d.Class != "lint" || d.Policy != checks.PolicyOptional {
		t.Fatalf("unexpected mapping: %+v", d)
	}
}
