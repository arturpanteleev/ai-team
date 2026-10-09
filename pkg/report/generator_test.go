package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestReportsKeepCountsAndNormalizeTimestampsToUTC(t *testing.T) {
	reports := t.TempDir()
	artifactsRoot := t.TempDir()
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("UTC+7", 7*60*60))
	end := time.Date(2026, 10, 9, 15, 0, 0, 0, time.FixedZone("UTC-4", -4*60*60))
	stage := notifier.StageResult{
		RunID: "run-timezones", AttemptID: "run-timezones-001-reviewer", Name: "reviewer",
		Status: notifier.StatusPassed, StageIndex: 1, TotalStages: 1, StartedAt: start, FinishedAt: end,
		Inputs:  []workflow.Artifact{{Name: "proposal", Path: filepath.Join(artifactsRoot, "proposal.md"), Size: 12}},
		Outputs: []workflow.Artifact{{Name: "review", Path: filepath.Join(artifactsRoot, "review.md"), Size: 34}},
		Checks:  []checks.Result{{Name: "lint", Class: "static", Policy: "required", Status: "passed", Command: []string{"go", "vet", "./..."}}},
	}
	if err := GenerateStageReport(reports, "feature", stage.AttemptID, stage, artifactsRoot); err != nil {
		t.Fatal(err)
	}
	stageHTML, err := os.ReadFile(filepath.Join(reports, "feature", "attempts", stage.AttemptID, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proposal.md", "review.md", "lint", "go vet ./..."} {
		if !strings.Contains(string(stageHTML), want) {
			t.Errorf("stage report missing %q", want)
		}
	}
	if err := GenerateFinalReport(reports, "feature", []notifier.StageResult{stage}, start, end, artifactsRoot, "completed"); err != nil {
		t.Fatal(err)
	}
	finalHTML, err := os.ReadFile(filepath.Join(reports, "feature", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2026-10-09T05:00:00Z", "2026-10-09T19:00:00Z", "14h0m0s", "<td>1</td>"} {
		if !strings.Contains(string(finalHTML), want) {
			t.Errorf("final report missing %q", want)
		}
	}
	if strings.Contains(string(finalHTML), "+07:00") || strings.Contains(string(finalHTML), "-04:00") {
		t.Fatalf("timestamps must be rendered in one timezone: %s", finalHTML)
	}
}

func TestGenerateFinalReportPreservesOutcomeCategories(t *testing.T) {
	reports := t.TempDir()
	stages := []notifier.StageResult{
		{RunID: "run-123", Name: "ok", Status: notifier.StatusPassed},
		{Name: "failed", Status: notifier.StatusFailed, Err: errors.New("boom")},
		{Name: "blocked", Status: notifier.StatusBlocked},
		{Name: "skipped", Status: notifier.StatusSkipped},
		{Name: "warning", Status: notifier.StatusWarning},
		{Name: "old", Status: notifier.StatusPassed, Superseded: true},
	}
	if err := GenerateFinalReport(reports, "feature", stages, time.Unix(1, 0), time.Unix(2, 0), t.TempDir(), "failed"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(reports, "feature", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{
		"Run ID:</strong> <span style=\"font-family:monospace\">run-123",
		"Passed</div><div class=\"summary-value status-ok\">1",
		"Failed</div><div class=\"summary-value status-err\">1",
		"Blocked</div><div class=\"summary-value status-blocked\">1",
		"Stopped / skipped</div><div class=\"summary-value status-warning\">1",
		"Warnings</div><div class=\"summary-value status-warning\">1",
		"Invalidated</div><div class=\"summary-value status-warning\">1",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("final report missing %q", want)
		}
	}
}
