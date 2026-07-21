package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

//go:embed templates/stage.html
var stageTemplateSrc string

//go:embed templates/final.html
var finalTemplateSrc string

var (
	stageTemplate = template.Must(template.New("stage").Parse(stageTemplateSrc))
	finalTemplate = template.Must(template.New("final").Parse(finalTemplateSrc))
)

type stageData struct {
	RunID       string
	AttemptID   string
	Feature     string
	Agent       string
	Status      string
	StatusClass string
	StatusEmoji string
	Verdict     string
	Blocker     string
	Duration    string
	StageIndex  int
	TotalStages int
	Error       string
	Summary     string
	Execution   string
	Decision    string
	Outcome     string
	Inputs      []artifactData
	Outputs     []artifactData
	Checks      []checkData
	Mutations   []string
	Delivery    any
}

type checkData struct {
	Name, Class, Policy, Status, Command, Reason, Duration string
	ExitCode                                               int
}

type artifactData struct {
	Name         string
	Path         string
	RelativePath string
	Size         int64
}

type finalData struct {
	RunID              string
	Feature            string
	StartTime          string
	EndTime            string
	OverallStatus      string
	OverallStatusClass string
	OverallStatusEmoji string
	TotalStages        int
	Passed             int
	Failed             int
	Blocked            int
	Stopped            int
	Warnings           int
	Invalidated        int
	TotalDuration      string
	Stages             []finalStageData
}

type finalStageData struct {
	Name        string
	AttemptID   string
	StageIndex  int
	Status      string
	StatusClass string
	StatusEmoji string
	Verdict     string
	Duration    string
	InputCount  int
	OutputCount int
	Summary     string
	Outcome     string
	CheckCount  int
	ReportPath  string
}

// GenerateStageReport создаёт HTML-отчёт этапа. Вызывается для всех исходов
// этапа (успех, ошибка, blocked).
func GenerateStageReport(reportsDir, feature, attemptID string, result notifier.StageResult, artifactsRoot string) error {
	if attemptID == "" || filepath.Base(attemptID) != attemptID {
		return fmt.Errorf("invalid report attempt id %q", attemptID)
	}
	dir := filepath.Join(reportsDir, feature, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := stageData{
		RunID:       result.RunID,
		AttemptID:   result.AttemptID,
		Feature:     feature,
		Agent:       result.Name,
		Status:      statusText(result),
		StatusClass: statusClass(result),
		StatusEmoji: statusEmoji(result),
		Verdict:     string(result.Verdict),
		Blocker:     result.Blocker,
		Duration:    result.Duration.Round(time.Second).String(),
		StageIndex:  result.StageIndex,
		TotalStages: result.TotalStages,
		Summary:     result.Summary,
		Execution:   string(result.State.Execution),
		Decision:    string(result.State.Decision),
		Outcome:     string(result.State.Outcome),
		Mutations:   append([]string(nil), result.Mutations...),
		Delivery:    result.Delivery,
	}

	if result.Err != nil {
		data.Error = result.Err.Error()
	}

	for _, in := range result.Inputs {
		data.Inputs = append(data.Inputs, toArtifactData(in.Name, in.Path, in.Size, artifactsRoot))
	}
	for _, out := range result.Outputs {
		data.Outputs = append(data.Outputs, toArtifactData(out.Name, out.Path, out.Size, artifactsRoot))
	}
	for _, check := range result.Checks {
		data.Checks = append(data.Checks, checkData{
			Name: check.Name, Class: check.Class, Policy: check.Policy, Status: check.Status,
			Command: strings.Join(check.Command, " "), Reason: check.Reason,
			Duration: check.Duration.Round(time.Millisecond).String(), ExitCode: check.ExitCode,
		})
	}

	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()

	return stageTemplate.Execute(f, data)
}

// toArtifactData вычисляет путь артефакта относительно artifactsRoot —
// шаблон строит из него ссылку ../../../artifacts/{RelativePath}
// (отчёт лежит в reports/{feature}/{agent}/index.html).
func toArtifactData(name, fullPath string, size int64, artifactsRoot string) artifactData {
	rel, err := filepath.Rel(artifactsRoot, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = ""
	}
	return artifactData{
		Name:         name,
		Path:         fullPath,
		RelativePath: filepath.ToSlash(rel),
		Size:         size,
	}
}

func GenerateFinalReport(reportsDir, feature string, stages []notifier.StageResult, startTime, endTime time.Time, artifactsRoot, runStatus string) error {
	dir := filepath.Join(reportsDir, feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := finalData{
		Feature:   feature,
		StartTime: startTime.Format(time.RFC3339),
		EndTime:   endTime.Format(time.RFC3339),
	}
	if len(stages) > 0 {
		data.RunID = stages[0].RunID
	}

	var totalDuration time.Duration

	for _, s := range stages {
		switch {
		case s.Superseded || s.Status == notifier.StatusInvalidated:
			data.Invalidated++
		case s.Status == notifier.StatusBlocked:
			data.Blocked++
		case s.Status == notifier.StatusSkipped || s.Status == notifier.StatusCanceled:
			data.Stopped++
		case s.Status == notifier.StatusWarning:
			data.Warnings++
		case s.Err == nil && s.Status == notifier.StatusPassed:
			data.Passed++
		default:
			data.Failed++
		}
		totalDuration += s.Duration

		data.Stages = append(data.Stages, finalStageData{
			Name:        s.Name,
			AttemptID:   s.AttemptID,
			StageIndex:  s.StageIndex,
			Status:      statusText(s),
			StatusClass: statusClass(s),
			StatusEmoji: statusEmoji(s),
			Verdict:     string(s.Verdict),
			Duration:    s.Duration.Round(time.Second).String(),
			InputCount:  len(s.Inputs),
			OutputCount: len(s.Outputs),
			Summary:     s.Summary,
			Outcome:     string(s.State.Outcome),
			CheckCount:  len(s.Checks),
			ReportPath:  filepath.ToSlash(filepath.Join("attempts", s.AttemptID, "index.html")),
		})
	}

	data.OverallStatus, data.OverallStatusClass, data.OverallStatusEmoji = runPresentation(runStatus)
	data.TotalStages = len(stages)
	data.TotalDuration = totalDuration.Round(time.Second).String()

	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()

	return finalTemplate.Execute(f, data)
}

func statusText(r notifier.StageResult) string {
	switch {
	case r.Status == notifier.StatusBlocked:
		return "Blocked"
	case r.Status == notifier.StatusRejected:
		return "Rejected"
	case r.Status == notifier.StatusCanceled:
		return "Canceled"
	case r.Status == notifier.StatusWarning:
		return "Warning"
	case r.Status == notifier.StatusSkipped:
		return "Skipped"
	case r.Superseded || r.Status == notifier.StatusInvalidated:
		return "Invalidated"
	case r.Status == notifier.StatusPassed && r.Err == nil:
		return "Passed"
	default:
		return "Failed"
	}
}

func statusClass(r notifier.StageResult) string {
	switch {
	case r.Status == notifier.StatusBlocked:
		return "blocked"
	case r.Status == notifier.StatusWarning:
		return "warning"
	case r.Status == notifier.StatusSkipped:
		return "warning"
	case r.Superseded || r.Status == notifier.StatusInvalidated:
		return "warning"
	case r.Status == notifier.StatusPassed && r.Err == nil:
		return "ok"
	default:
		return "err"
	}
}

func statusEmoji(r notifier.StageResult) string {
	switch {
	case r.Status == notifier.StatusBlocked:
		return "⊘"
	case r.Status == notifier.StatusWarning:
		return "!"
	case r.Status == notifier.StatusSkipped:
		return "⏸"
	case r.Superseded || r.Status == notifier.StatusInvalidated:
		return "↺"
	case r.Status == notifier.StatusPassed && r.Err == nil:
		return "✓"
	default:
		return "✗"
	}
}

func runPresentation(status string) (text, class, emoji string) {
	switch status {
	case "completed":
		return "Passed", "ok", "✓"
	case "completed_with_warnings":
		return "Completed with warnings", "warning", "!"
	case "blocked":
		return "Blocked", "blocked", "⊘"
	case "stopped":
		return "Stopped", "warning", "■"
	case "canceled":
		return "Canceled", "warning", "■"
	default:
		return "Failed", "err", "✗"
	}
}

// ReadStageSummary читает summary этапа из
// {artifactsRoot}/{feature}/.stage-summary/{agent}.md; обрезка — по рунам.
func ReadStageSummary(artifactsRoot, feature, agent string) string {
	path := filepath.Join(artifactsRoot, feature, ".stage-summary", agent+".md")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ""
	}
	data, err := safeio.ReadRegularFile(path, 64<<10)
	if err != nil {
		return ""
	}
	return ui.Truncate(strings.TrimSpace(string(data)), 200)
}
