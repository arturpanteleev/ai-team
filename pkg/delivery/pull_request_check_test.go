package delivery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidatePullRequestRules(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantURL string
		wantErr string
	}{
		{
			name:    "open on expected base",
			output:  `{"url":"https://github.test/team/repo/pull/12","state":"OPEN","baseRefName":"main","headRefName":"feature","headRefOid":"abc"}`,
			wantURL: "https://github.test/team/repo/pull/12",
		},
		{
			name:    "missing PR URL",
			output:  `{"url":"","state":"OPEN","baseRefName":"main","headRefName":"feature"}`,
			wantErr: "pr_exists",
		},
		{
			name:    "closed PR",
			output:  `{"url":"https://github.test/team/repo/pull/12","state":"CLOSED","baseRefName":"main","headRefName":"feature"}`,
			wantErr: "pr_open",
		},
		{
			name:    "wrong base",
			output:  `{"url":"https://github.test/team/repo/pull/12","state":"OPEN","baseRefName":"develop","headRefName":"feature"}`,
			wantErr: "pr_base_branch",
		},
		{
			name:    "trailing JSON is rejected",
			output:  `{"url":"https://github.test/team/repo/pull/12","state":"OPEN","baseRefName":"main","headRefName":"feature"} {}`,
			wantErr: "multiple JSON values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotURL, err := ValidatePullRequest(test.output, "main")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ValidatePullRequest error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil || gotURL != test.wantURL {
				t.Fatalf("ValidatePullRequest = %q, %v; want %q, nil", gotURL, err, test.wantURL)
			}
		})
	}
}

func TestCheckPullRequestWithGHStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh fixture uses a Unix shell script")
	}
	tests := []struct {
		name        string
		exitCode    string
		response    string
		wantStatus  []string
		wantFailure string
	}{
		{
			name:        "missing PR",
			exitCode:    "1",
			wantStatus:  []string{"failed", "skipped", "skipped"},
			wantFailure: "pr_exists",
		},
		{
			name:        "closed PR",
			exitCode:    "0",
			response:    `{"url":"https://github.test/team/repo/pull/12","state":"CLOSED","baseRefName":"main","headRefName":"feature"}`,
			wantStatus:  []string{"passed", "failed", "passed"},
			wantFailure: "pr_open",
		},
		{
			name:        "wrong base",
			exitCode:    "0",
			response:    `{"url":"https://github.test/team/repo/pull/12","state":"OPEN","baseRefName":"develop","headRefName":"feature"}`,
			wantStatus:  []string{"passed", "passed", "failed"},
			wantFailure: "pr_base_branch",
		},
		{
			name:       "valid PR",
			exitCode:   "0",
			response:   `{"url":"https://github.test/team/repo/pull/12","state":"OPEN","baseRefName":"main","headRefName":"feature"}`,
			wantStatus: []string{"passed", "passed", "passed"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installPullRequestGHStub(t, test.exitCode, test.response)
			report, err := CheckPullRequest(context.Background(), ExecRunner{}, t.TempDir(), "https://github.test/team/repo/pull/12", "main")
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Rules) != len(test.wantStatus) {
				t.Fatalf("got %d rules, want %d: %+v", len(report.Rules), len(test.wantStatus), report.Rules)
			}
			for i, want := range test.wantStatus {
				if report.Rules[i].Status != want {
					t.Errorf("%s status=%q, want %q (report %+v)", report.Rules[i].Rule, report.Rules[i].Status, want, report)
				}
				if i < len(test.wantStatus)-1 && report.Rules[i].Rule == "" {
					t.Errorf("rule %d has no name", i)
				}
			}
			if test.wantFailure != "" {
				index := ruleIndex(report.Rules, test.wantFailure)
				if index < 0 || report.Rules[index].Status != "failed" {
					t.Fatalf("expected %s failure, report=%+v", test.wantFailure, report)
				}
			}
		})
	}
}

func installPullRequestGHStub(t *testing.T, exitCode, response string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$AI_TEAM_GH_EXIT" != "0" ]; then
  exit "$AI_TEAM_GH_EXIT"
fi
printf '%s\n' "$AI_TEAM_GH_RESPONSE"
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AI_TEAM_GH_EXIT", exitCode)
	t.Setenv("AI_TEAM_GH_RESPONSE", response)
}

func ruleIndex(rules []PullRequestRuleResult, name string) int {
	for i, rule := range rules {
		if rule.Rule == name {
			return i
		}
	}
	return -1
}
