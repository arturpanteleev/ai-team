package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// classified checks that every exported field of typ has a class in classes and
// that classes names no field that does not exist.
func classified(t *testing.T, typ reflect.Type, classes map[string]fieldClass) {
	t.Helper()
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		seen[field.Name] = true
		class, ok := classes[field.Name]
		if !ok {
			t.Errorf("%s.%s is not classified for web submissions: add it to the allow-list table in trusted_commands.go "+
				"(processField only if a template author may change it without touching the host, its tokens, endpoints or commands)", typ.Name(), field.Name)
			continue
		}
		if tag := field.Tag.Get("yaml"); tag == "-" && class != derivedField {
			t.Errorf("%s.%s is yaml:\"-\" and must be derivedField", typ.Name(), field.Name)
		}
		if tag := field.Tag.Get("yaml"); tag != "-" && class == derivedField {
			t.Errorf("%s.%s is read from YAML and cannot be derivedField", typ.Name(), field.Name)
		}
	}
	for name := range classes {
		if !seen[name] {
			t.Errorf("%s classifies %q, which is not a field of %s", typ.Name(), name, typ.Name())
		}
	}
}

// The guard: a new configuration field must be classified, otherwise it could
// reach the web unreviewed.
func TestConfigFieldClassification(t *testing.T) {
	classified(t, reflect.TypeOf(Config{}), configFieldClasses)
	classified(t, reflect.TypeOf(TemplateStage{}), stageFieldClasses)
	classified(t, reflect.TypeOf(TrackerConfig{}), trackerFieldClasses)
	classified(t, reflect.TypeOf(JiraConfig{}), jiraFieldClasses)
}

func TestConfigProcessFieldsAreTheReviewedSet(t *testing.T) {
	var process []string
	for name, class := range configFieldClasses {
		if class == processField {
			process = append(process, name)
		}
	}
	want := "SchemaVersion StallAfter Template Title"
	sortStrings(process)
	if got := strings.Join(process, " "); got != want {
		t.Fatalf("the process allow-list changed (review the security impact first):\n got %s\nwant %s", got, want)
	}
}

func sortStrings(values []string) {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

const trustedBaseYAML = `schema_version: 6
template: base
title: Base
stages:
  - {id: spec, title: Spec, function: po, result: md, executor: human}
  - {id: build, title: Build, function: developer, result: md, executor: human}
returns:
  - {from: build, to: spec}
cli: opencode
model: m1
stage_timeout: 30m
containment: {profile: strict}
tree_hash: {ignore_dirs: [node_modules]}
budget: {max_attempts: 5}
redaction: {scan_sensitive_fields: true}
retention: {keep_last: 10}
tracker:
  adapter: jira
  web_base_url: https://ai.example.test
  on_enter: {spec: In Spec}
  on_pass: {spec: Done Spec}
  inbound:
    poll_interval: 1m
    rules:
      - {status: Go, action: start, stage: spec}
  jira:
    base_url: https://jira.example.test
    email: bot@example.test
    token_env: JIRA_TOKEN
    web_base_url: https://ai.example.test
    stage_statuses: {build: Done Build}
cicd:
  teamcity:
    base_url: https://tc.example.test
    build_type: Build
    token_env: TC_TOKEN
    branch: main
notifications:
  channels:
    - {name: slack, type: slack, url_env: SLACK_URL, events: [stage_stalled]}
`

func parseTrusted(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWebSubmissionAcceptsIdenticalAndProcessChanges(t *testing.T) {
	trusted := parseTrusted(t, trustedBaseYAML)
	if problems := parseTrusted(t, trustedBaseYAML).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("a document identical to the file config must be accepted: %v", problems)
	}
	process := strings.NewReplacer(
		"title: Base", "title: Renamed",
		"on_enter: {spec: In Spec}", "on_enter: {spec: Elsewhere, build: Doing}",
		"on_pass: {spec: Done Spec}", "on_pass: {build: Shipped}",
		"stage_statuses: {build: Done Build}", "stage_statuses: {build: Other}",
	).Replace(trustedBaseYAML) + "max_visits: {spec: 2}\nforbid_self_approval: true\nstall_after: 2h\n"
	if problems := parseTrusted(t, process).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("process changes (titles, tracker status maps, stricter limits) must be accepted: %v", problems)
	}
}

func TestWebSubmissionRefusesInfrastructureChanges(t *testing.T) {
	trusted := parseTrusted(t, trustedBaseYAML)
	cases := map[string]struct{ from, to, field string }{
		"jira base url":      {"base_url: https://jira.example.test", "base_url: https://evil.example", "tracker.jira.base_url"},
		"jira token env":     {"token_env: JIRA_TOKEN", "token_env: AWS_SECRET_ACCESS_KEY", "tracker.jira.token_env"},
		"jira email":         {"email: bot@example.test", "email: x@example.test", "tracker.jira.email"},
		"tracker adapter":    {"adapter: jira", "adapter: fake", "tracker.adapter"},
		"tracker web url":    {"web_base_url: https://ai.example.test\n  on_enter", "web_base_url: https://evil.example\n  on_enter", "tracker.web_base_url"},
		"tracker inbound":    {"{status: Go, action: start, stage: spec}", "{status: Go, action: start, stage: build}", "tracker.inbound"},
		"teamcity base url":  {"base_url: https://tc.example.test", "base_url: https://evil.example", "cicd.teamcity.base_url"},
		"teamcity token env": {"token_env: TC_TOKEN", "token_env: HOME", "cicd.teamcity.token_env"},
		"notification env":   {"url_env: SLACK_URL", "url_env: GITHUB_TOKEN", "notifications.channels"},
		"containment":        {"containment: {profile: strict}", "containment: {profile: trusted-local}", "containment.profile"},
		"cli":                {"cli: opencode", "cli: /tmp/evil", "cli"},
		"model":              {"model: m1", "model: m2", "model"},
		"stage timeout":      {"stage_timeout: 30m", "stage_timeout: 1h", "stage_timeout"},
		"tree hash":          {"ignore_dirs: [node_modules]", "ignore_dirs: [src]", "tree_hash.ignore_dirs"},
		"budget":             {"max_attempts: 5", "max_attempts: 500", "budget.max_attempts"},
		"redaction":          {"scan_sensitive_fields: true", "scan_sensitive_fields: false", "redaction.scan_sensitive_fields"},
		"retention":          {"keep_last: 10", "keep_last: 0", "retention"},
	}
	for name, tc := range cases {
		if !strings.Contains(trustedBaseYAML, tc.from) {
			t.Fatalf("%s: fixture lost %q", name, tc.from)
		}
		changed := strings.Replace(trustedBaseYAML, tc.from, tc.to, 1)
		problems := parseTrusted(t, changed).UntrustedCommandProblems(trusted)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.field) {
			t.Errorf("%s: want a problem naming %s, got %v", name, tc.field, problems)
		}
	}
	// New sections the file config does not have.
	for name, extra := range map[string]string{
		"auto pickup": "auto_pickup: {enabled: true}\n",
		"usage":       "usage: {monthly_subscription_amount: 1, monthly_subscription_currency: USD}\n",
	} {
		problems := parseTrusted(t, trustedBaseYAML+extra).UntrustedCommandProblems(trusted)
		if len(problems) == 0 {
			t.Errorf("%s: a new infrastructure section must be refused", name)
		}
	}
	// Removing a section is a change too.
	if problems := parseTrusted(t, strings.Replace(trustedBaseYAML, "containment: {profile: strict}\n", "", 1)).UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Error("dropping containment must be refused")
	}
}

func TestWebSubmissionWithoutFileConfigRefusesEverySection(t *testing.T) {
	problems := parseTrusted(t, trustedBaseYAML).UntrustedCommandProblems(nil)
	joined := strings.Join(problems, "\n")
	for _, section := range []string{"containment", "tracker", "cicd", "notifications", "cli", "budget", "redaction"} {
		if !strings.Contains(joined, section) {
			t.Errorf("without a trusted file %s must be refused: %v", section, problems)
		}
	}
	if problems := parseTrusted(t, strings.SplitN(trustedBaseYAML, "cli:", 2)[0]).UntrustedCommandProblems(nil); len(problems) != 0 {
		t.Errorf("a pure process template needs no file config: %v", problems)
	}
}

// The demo publishes its short templates through the web over the full demo
// file config: they must stay acceptable (process changes only) while the full
// demo template, with command gates, must stay refused.
func TestDemoTemplatesAreAcceptedFromTheWeb(t *testing.T) {
	read := func(name string) *Config {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "demo", name))
		if err != nil {
			t.Fatal(err)
		}
		return parseTrusted(t, string(data))
	}
	file := read("demo-template.yaml")
	for _, name := range []string{"demo-template-review-loop-external.yaml", "demo-template-review-loop-internal.yaml"} {
		short := read(name)
		if problems := short.UntrustedCommandProblems(short); len(problems) != 0 {
			t.Errorf("%s must be accepted by the web when it is the file configuration: %v", name, problems)
		}
		// Against the full demo file the short template drops gates, which the
		// web may not do; the demo script makes the short template the file first.
		if problems := short.UntrustedCommandProblems(file); !strings.Contains(strings.Join(problems, "\n"), "нельзя убрать") {
			t.Errorf("%s drops gates of the full demo file and must be refused: %v", name, problems)
		}
	}
	if problems := file.UntrustedCommandProblems(file); len(problems) == 0 {
		t.Error("the full demo template carries command gates and must be refused from the web")
	}
}

func TestWebSubmissionDeliveryPolicyMustMatch(t *testing.T) {
	withDelivery := strings.Replace(trustedBaseYAML,
		"{id: build, title: Build, function: developer, result: md, executor: human}",
		"{id: build, title: Build, function: developer, result: md, executor: human, delivery: {require_checks: [unit]}}", 1)
	trusted := parseTrusted(t, withDelivery)
	if problems := parseTrusted(t, withDelivery).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("identical delivery must be accepted: %v", problems)
	}
	weakened := strings.Replace(withDelivery, "require_checks: [unit]", "require_checks: []", 1)
	if problems := parseTrusted(t, weakened).UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Fatal("weakened delivery must be refused")
	}
	if problems := parseTrusted(t, trustedBaseYAML).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("a stage without delivery delivers nothing and is allowed: %v", problems)
	}
	if problems := parseTrusted(t, withDelivery).UntrustedCommandProblems(parseTrusted(t, trustedBaseYAML)); len(problems) == 0 {
		t.Fatal("a new delivery policy must be refused")
	}
}

func TestWebSubmissionRefusesLoosenedProcessSecurity(t *testing.T) {
	strict := strings.Replace(trustedBaseYAML, "returns:", "forbid_self_approval: true\nmax_visits: {spec: 2}\nreturns:", 1)
	trusted := parseTrusted(t, strict)
	if problems := parseTrusted(t, strict).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("identical document must be accepted: %v", problems)
	}
	cases := map[string]struct{ from, to, field string }{
		"self approval off": {"forbid_self_approval: true\n", "", "forbid_self_approval"},
		"visits raised":     {"max_visits: {spec: 2}", "max_visits: {spec: 9}", "max_visits[spec]"},
		"role changed":      {"function: developer", "function: architect", "function"},
		"approver added":    {"function: developer,", "function: developer, approver_function: po,", "approver_function"},
		"executor agent":    {"{id: build, title: Build, function: developer, result: md, executor: human}", "{id: build, title: Build, function: developer, result: md, executor: agent, agent: coder}", "executor"},
		"new return route":  {"- {from: build, to: spec}", "- {from: build, to: spec}\n  - {from: spec, to: spec}", "returns"},
	}
	for name, tc := range cases {
		if !strings.Contains(strict, tc.from) {
			t.Fatalf("%s: fixture lost %q", name, tc.from)
		}
		changed := strings.Replace(strict, tc.from, tc.to, 1)
		cfg, err := ParseYAML([]byte(changed))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		problems := cfg.UntrustedCommandProblems(trusted)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.field) {
			t.Errorf("%s: want a problem naming %s, got %v", name, tc.field, problems)
		}
	}
	// A stage added from the web: human only, known roles.
	added := strings.Replace(strict, "forbid_self_approval:", "  - {id: review, title: Review, function: developer, result: md, executor: human}\nforbid_self_approval:", 1)
	if problems := parseTrusted(t, added).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Errorf("a new human stage with known roles is process: %v", problems)
	}
	newRole := strings.Replace(added, "id: review, title: Review, function: developer", "id: review, title: Review, function: auditor", 1)
	if problems := parseTrusted(t, newRole).UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Error("a new role must be refused")
	}
	withAgent := strings.Replace(added, "id: review, title: Review, function: developer, result: md, executor: human", "id: review, title: Review, function: developer, result: md, executor: agent, agent: coder", 1)
	if cfg, err := ParseYAML([]byte(withAgent)); err == nil && len(cfg.UntrustedCommandProblems(trusted)) == 0 {
		t.Error("a new agent stage must be refused")
	}
	// Dropping an adapter or narrowing the MCP servers is less privileged.
	if !subsetOf([]string{"a"}, []string{"a", "b"}) || subsetOf([]string{"c"}, []string{"a"}) {
		t.Error("subsetOf is wrong")
	}
}

// An absent tracker (or jira) section and an empty one are the same
// configuration: adding or dropping an empty section is not a change.
func TestWebSubmissionTrackerNilEqualsZero(t *testing.T) {
	base := strings.SplitN(trustedBaseYAML, "tracker:", 2)[0]
	trusted := parseTrusted(t, base)
	for name, extra := range map[string]string{
		"empty tracker":        "tracker: {}\n",
		"empty jira":           "tracker:\n  jira: {}\n",
		"status maps only":     "tracker:\n  on_enter: {spec: Doing}\n  jira:\n    stage_statuses: {build: Done}\n",
		"tracker without jira": "tracker:\n  on_pass: {spec: Done}\n",
	} {
		if problems := parseTrusted(t, base+extra).UntrustedCommandProblems(trusted); len(problems) != 0 {
			t.Errorf("%s over an empty file section must be accepted: %v", name, problems)
		}
	}
	withEmpty := parseTrusted(t, base+"tracker: {}\n")
	if problems := parseTrusted(t, base).UntrustedCommandProblems(withEmpty); len(problems) != 0 {
		t.Errorf("dropping an empty tracker section must be accepted: %v", problems)
	}
	if problems := parseTrusted(t, base+"tracker:\n  adapter: fake\n").UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Error("a real tracker adapter over an empty file section must still be refused")
	}
	if problems := parseTrusted(t, base+"tracker:\n  jira:\n    base_url: https://x.example\n").UntrustedCommandProblems(trusted); len(problems) == 0 {
		t.Error("a jira endpoint over an empty file section must still be refused")
	}
}

// Through the web the stage can only get stricter: not skippable, no gate or
// confirmation removed; an empty executor of the file is a human stage.
func TestWebSubmissionTightensStageFieldsOnly(t *testing.T) {
	gated := strings.Replace(trustedBaseYAML,
		"{id: spec, title: Spec, function: po, result: md, executor: human}",
		`{id: spec, title: Spec, function: po, result: md, exit_gate_groups: [[{gate: required_sections, version: 1, params: {sections: [A]}}], [{gate: human_approval, version: 2, name: ok}]]}`, 1)
	trusted := parseTrusted(t, gated)
	if problems := parseTrusted(t, gated).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Fatalf("an identical document, empty executor included, must be accepted: %v", problems)
	}
	humanExecutor := strings.Replace(gated, "result: md, exit_gate_groups", "result: md, executor: human, exit_gate_groups", 1)
	if problems := parseTrusted(t, humanExecutor).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Errorf("an empty executor equals human: %v", problems)
	}
	tighter := strings.Replace(gated, "[{gate: human_approval, version: 2, name: ok}]]", "[{gate: human_approval, version: 2, name: ok}], [{gate: required_phrases, version: 1, params: {phrases: [x]}}]]", 1)
	if problems := parseTrusted(t, tighter).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Errorf("adding a gate only tightens: %v", problems)
	}
	noStage := strings.Replace(gated, "  - {id: build, title: Build, function: developer, result: md, executor: human}\nreturns:\n  - {from: build, to: spec}\n", "", 1)
	if problems := parseTrusted(t, noStage).UntrustedCommandProblems(trusted); len(problems) != 0 {
		t.Errorf("deleting a stage stays allowed: %v", problems)
	}
	refused := map[string]struct{ from, to, field string }{
		"confirm removed":   {", [{gate: human_approval, version: 2, name: ok}]", "", "ok"},
		"gate removed":      {"[{gate: required_sections, version: 1, params: {sections: [A]}}], ", "", "required_sections"},
		"gate params":       {"sections: [A]", "sections: [B]", "required_sections"},
		"skippable enabled": {"result: md, exit_gate_groups", "result: md, skippable: true, exit_gate_groups", "skippable"},
	}
	for name, tc := range refused {
		if !strings.Contains(gated, tc.from) {
			t.Fatalf("%s: fixture lost %q", name, tc.from)
		}
		problems := parseTrusted(t, strings.Replace(gated, tc.from, tc.to, 1)).UntrustedCommandProblems(trusted)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.field) {
			t.Errorf("%s: want a problem naming %s, got %v", name, tc.field, problems)
		}
	}
}
