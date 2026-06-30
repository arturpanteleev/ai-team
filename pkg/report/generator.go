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
)

//go:embed templates/stage.html
var stageTemplate string

//go:embed templates/final.html
var finalTemplate string

type stageData struct {
	Feature     string
	Agent       string
	Status      string
	StatusClass string
	StatusEmoji string
	Duration    string
	StageIndex  int
	TotalStages int
	Error       string
	Summary     string
	Inputs      []artifactData
	Outputs     []artifactData
}

type artifactData struct {
	Name         string
	Path         string
	RelativePath string
	Size         int64
}

type finalData struct {
	Feature            string
	StartTime          string
	EndTime            string
	OverallStatus      string
	OverallStatusClass string
	OverallStatusEmoji string
	TotalStages        int
	Passed             int
	Failed             int
	TotalDuration      string
	Stages             []finalStageData
}

type finalStageData struct {
	Name         string
	Agent        string
	StageIndex   int
	Status       string
	StatusClass  string
	StatusEmoji  string
	Duration     string
	InputCount   int
	OutputCount  int
	Summary      string
}

func GenerateStageReport(reportsDir, feature, agent string, result notifier.StageResult, artifactsRoot string) error {
	dir := filepath.Join(reportsDir, feature, agent)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := stageData{
		Feature:     feature,
		Agent:       agent,
		Status:      statusText(result.Err == nil),
		StatusClass: statusClass(result.Err == nil),
		StatusEmoji: statusEmoji(result.Err == nil),
		Duration:    result.Duration.Round(time.Second).String(),
		StageIndex:  result.StageIndex,
		TotalStages: result.TotalStages,
	}

	if result.Err != nil {
		data.Error = result.Err.Error()
	}

	data.Summary = readStageSummary(artifactsRoot, feature, agent)

	relFromReports := func(artifactRoot, fullPath string) string {
		rel := strings.TrimPrefix(fullPath, artifactRoot)
		rel = strings.TrimPrefix(rel, "/")
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) >= 2 {
			return parts[1]
		}
		return rel
	}

	for _, in := range result.Inputs {
		data.Inputs = append(data.Inputs, artifactData{
			Name:         in.Name,
			Path:         in.Path,
			RelativePath: relFromReports(artifactsRoot, in.Path),
			Size:         in.Size,
		})
	}

	for _, out := range result.Outputs {
		data.Outputs = append(data.Outputs, artifactData{
			Name:         out.Name,
			Path:         out.Path,
			RelativePath: relFromReports(artifactsRoot, out.Path),
			Size:         out.Size,
		})
	}

	tmpl, err := template.New("stage").Parse(stageTemplate)
	if err != nil {
		return fmt.Errorf("parse stage template: %w", err)
	}

	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}

func GenerateFinalReport(reportsDir, feature string, stages []notifier.StageResult, startTime, endTime time.Time, artifactsRoot string) error {
	dir := filepath.Join(reportsDir, feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := finalData{
		Feature:   feature,
		StartTime: startTime.Format(time.RFC3339),
		EndTime:   endTime.Format(time.RFC3339),
	}

	allOk := true
	passed := 0
	var totalDuration time.Duration

	for _, s := range stages {
		ok := s.Err == nil
		if ok {
			passed++
		} else {
			allOk = false
		}
		totalDuration += s.Duration

		inputCount := len(s.Inputs)
		outputCount := len(s.Outputs)

		summary := readStageSummary(artifactsRoot, feature, s.Name)

		data.Stages = append(data.Stages, finalStageData{
			Name:         s.Name,
			Agent:        s.Name,
			StageIndex:   s.StageIndex,
			Status:       statusText(ok),
			StatusClass:  statusClass(ok),
			StatusEmoji:  statusEmoji(ok),
			Duration:     s.Duration.Round(time.Second).String(),
			InputCount:   inputCount,
			OutputCount:  outputCount,
			Summary:      summary,
		})
	}

	data.OverallStatus = statusText(allOk)
	data.OverallStatusClass = statusClass(allOk)
	data.OverallStatusEmoji = statusEmoji(allOk)
	data.TotalStages = len(stages)
	data.Passed = passed
	data.Failed = len(stages) - passed
	data.TotalDuration = totalDuration.Round(time.Second).String()

	tmpl, err := template.New("final").Parse(finalTemplate)
	if err != nil {
		return fmt.Errorf("parse final template: %w", err)
	}

	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}

func statusText(ok bool) string {
	if ok {
		return "Passed"
	}
	return "Failed"
}

func statusClass(ok bool) string {
	if ok {
		return "ok"
	}
	return "err"
}

func statusEmoji(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func readStageSummary(artifactsRoot, feature, agent string) string {
	path := filepath.Join(artifactsRoot, ".stage-summary", agent+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
