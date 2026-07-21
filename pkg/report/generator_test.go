package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
)

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
