package eval

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Layer string

const (
	LayerDeterministic Layer = "deterministic_contract"
	LayerBehavioral    Layer = "behavioral_fixture"
	LayerFault         Layer = "fault_injection"
	LayerLLMQuality    Layer = "llm_quality"
)

type Case struct {
	Name     string
	Layer    Layer
	Advisory bool
	Run      func(context.Context) error
}

type CaseResult struct {
	Name       string        `json:"name"`
	Layer      Layer         `json:"layer"`
	Advisory   bool          `json:"advisory"`
	Passed     bool          `json:"passed"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	FinishedAt time.Time     `json:"finished_at"`
}

type SuiteResult struct {
	SchemaVersion int          `json:"schema_version"`
	Cases         []CaseResult `json:"cases"`
}

func RunSuite(ctx context.Context, cases []Case) (SuiteResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := SuiteResult{SchemaVersion: 1}
	seen := make(map[string]bool, len(cases))
	var hardFailures []string
	for _, testCase := range cases {
		if testCase.Name == "" || testCase.Run == nil {
			return result, fmt.Errorf("eval suite: case name and runner are required")
		}
		if seen[testCase.Name] {
			return result, fmt.Errorf("eval suite: duplicate case name %q", testCase.Name)
		}
		seen[testCase.Name] = true
		switch testCase.Layer {
		case LayerDeterministic, LayerBehavioral, LayerFault:
		case LayerLLMQuality:
			if !testCase.Advisory {
				return result, fmt.Errorf("eval suite: uncalibrated LLM quality case %s must be advisory", testCase.Name)
			}
		default:
			return result, fmt.Errorf("eval suite: unknown layer %q", testCase.Layer)
		}
		startedAt := time.Now()
		err := testCase.Run(ctx)
		caseResult := CaseResult{
			Name: testCase.Name, Layer: testCase.Layer, Advisory: testCase.Advisory,
			Passed: err == nil, Duration: time.Since(startedAt), FinishedAt: time.Now().UTC(),
		}
		if err != nil {
			caseResult.Error = err.Error()
			if !testCase.Advisory {
				hardFailures = append(hardFailures, testCase.Name)
			}
		}
		result.Cases = append(result.Cases, caseResult)
	}
	if len(hardFailures) > 0 {
		return result, fmt.Errorf("eval suite: %d hard case(s) failed: %s", len(hardFailures), strings.Join(hardFailures, ", "))
	}
	return result, nil
}
