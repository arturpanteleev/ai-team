package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// PullRequestRuleResult is the outcome of one of the built-in PR link rules.
type PullRequestRuleResult struct {
	Rule   string `json:"rule"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// PullRequestCheckReport is suitable for stage checks and keeps each rule
// separately visible to callers.
type PullRequestCheckReport struct {
	URL   string                  `json:"url,omitempty"`
	Rules []PullRequestRuleResult `json:"rules"`
}

type pullRequestView struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	HeadRefOID  string `json:"headRefOid"`
}

var pullRequestNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// CheckPullRequest evaluates pr_exists, pr_open, and pr_base_branch from a
// single gh response. reference can be a positive PR number or an HTTPS PR URL.
func CheckPullRequest(ctx context.Context, runner CommandRunner, targetDir, reference, expectedBaseBranch string) (PullRequestCheckReport, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	if err := validatePullRequestReference(reference); err != nil {
		return PullRequestCheckReport{}, err
	}
	if strings.TrimSpace(expectedBaseBranch) == "" {
		return PullRequestCheckReport{}, fmt.Errorf("pr_base_branch: expected base branch is required")
	}
	result := runner.Run(ctx, targetDir, "gh", "pr", "view", reference, "--json", "url,state,baseRefName,headRefName")
	if result.Status != StepPassed {
		if result.ExitCode == 1 {
			return PullRequestCheckReport{Rules: []PullRequestRuleResult{
				{Rule: "pr_exists", Status: "failed", Reason: "pull request was not found"},
				{Rule: "pr_open", Status: "skipped", Reason: "requires an existing pull request"},
				{Rule: "pr_base_branch", Status: "skipped", Reason: "requires an existing pull request"},
			}}, nil
		}
		reason := strings.TrimSpace(result.Stderr)
		if reason == "" {
			reason = result.Reason
		}
		return PullRequestCheckReport{}, fmt.Errorf("gh pr view failed (exit %d): %s", result.ExitCode, reason)
	}
	if result.Truncated {
		return PullRequestCheckReport{}, fmt.Errorf("gh pr view output was truncated")
	}
	view, err := decodePullRequestView(result.Stdout)
	if err != nil {
		return PullRequestCheckReport{}, err
	}
	return pullRequestRuleReport(view, expectedBaseBranch), nil
}

// ValidatePullRequest checks the same generic PR properties without tying the
// result to a delivery Plan. Delivery adds its own approved head/commit checks.
func ValidatePullRequest(output, expectedBaseBranch string) (string, error) {
	if strings.TrimSpace(expectedBaseBranch) == "" {
		return "", fmt.Errorf("pr_base_branch: expected base branch is required")
	}
	view, err := decodePullRequestView(output)
	if err != nil {
		return "", err
	}
	report := pullRequestRuleReport(view, expectedBaseBranch)
	for _, rule := range report.Rules {
		if rule.Status == "failed" {
			return "", fmt.Errorf("%s: %s", rule.Rule, rule.Reason)
		}
	}
	return report.URL, nil
}

func decodePullRequestView(output string) (pullRequestView, error) {
	var view pullRequestView
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&view); err != nil {
		return pullRequestView{}, fmt.Errorf("invalid gh pr view JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return pullRequestView{}, fmt.Errorf("invalid gh pr view JSON: multiple JSON values")
		}
		return pullRequestView{}, fmt.Errorf("invalid gh pr view JSON trailing data: %w", err)
	}
	return view, nil
}

func pullRequestRuleReport(view pullRequestView, expectedBaseBranch string) PullRequestCheckReport {
	report := PullRequestCheckReport{URL: view.URL}
	if err := validatePullRequestURL(view.URL); err != nil {
		report.Rules = []PullRequestRuleResult{
			{Rule: "pr_exists", Status: "failed", Reason: err.Error()},
			{Rule: "pr_open", Status: "skipped", Reason: "requires an existing pull request"},
			{Rule: "pr_base_branch", Status: "skipped", Reason: "requires an existing pull request"},
		}
		return report
	}
	report.Rules = []PullRequestRuleResult{{Rule: "pr_exists", Status: "passed"}}
	if view.State == "OPEN" {
		report.Rules = append(report.Rules, PullRequestRuleResult{Rule: "pr_open", Status: "passed"})
	} else {
		report.Rules = append(report.Rules, PullRequestRuleResult{Rule: "pr_open", Status: "failed", Reason: fmt.Sprintf("pull request state is %q, expected OPEN", view.State)})
	}
	if view.BaseRefName == expectedBaseBranch {
		report.Rules = append(report.Rules, PullRequestRuleResult{Rule: "pr_base_branch", Status: "passed"})
	} else {
		report.Rules = append(report.Rules, PullRequestRuleResult{Rule: "pr_base_branch", Status: "failed", Reason: fmt.Sprintf("base branch is %q, expected %q", view.BaseRefName, expectedBaseBranch)})
	}
	return report
}

func validatePullRequestReference(reference string) error {
	reference = strings.TrimSpace(reference)
	if pullRequestNumber.MatchString(reference) {
		return nil
	}
	parsed, err := url.ParseRequestURI(reference)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || !isPullRequestPath(parsed.Path) {
		return fmt.Errorf("pr_exists: reference must be a positive PR number or an HTTPS pull request URL")
	}
	return nil
}

func validatePullRequestURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || !isPullRequestPath(parsed.Path) {
		return fmt.Errorf("gh pr view returned an invalid pull request URL %q", value)
	}
	return nil
}

func isPullRequestPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) >= 2 && parts[len(parts)-2] == "pull" && pullRequestNumber.MatchString(parts[len(parts)-1])
}
