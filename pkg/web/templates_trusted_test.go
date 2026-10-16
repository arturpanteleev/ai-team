package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

const trustedImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

// trustedGates are the two command gates of the file config below.
const trustedGates = `    exit_gate_groups:
      - - gate: external_program
          version: 1
          name: host_check
          params: {command: ["sh", "scripts/gate.sh"]}
      - - gate: docker_program
          version: 1
          name: box_check
          params:
            image: ` + trustedImage + `
            command: ["/bin/sh", "/gate/repo/scripts/gate.sh"]
`

// trustedCommandsFileConfig is a repository file config that legitimately
// carries every command-bearing element: both command gates, an executor
// adapter and a project check. The file config accepts all of it; the web must
// not be able to add or change any of it (openspec external-gate).
const trustedCommandsFileConfig = `schema_version: 6
template: trusted
title: Trusted commands
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
` + trustedGates + `  - id: review
    title: Review
    function: reviewer
    result: md
    executor: agent
    agent: demo-reviewer
    adapter: box-review
returns:
  - {from: review, to: spec}
executor_adapters:
  box-review:
    argv: ["/bin/sh", "/agent/review.sh"]
    timeout: 2m
    docker:
      image: ` + trustedImage + `
      network: none
      mounts: ["agent:/agent"]
checks:
  - {name: unit, class: unit, command: [go, test, ./...], policy: required, timeout: 5m}
`

const trustedPlainTemplate = `schema_version: 6
template: plain
title: Plain
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
  - id: build
    title: Build
    function: developer
    result: md
    executor: human
returns:
  - {from: build, to: spec}
`

// webTemplateAttempts are submissions every web route must refuse; each names
// something the file config alone may define.
func webTemplateAttempts() map[string]string {
	withGate := func(gate string) string {
		return strings.Replace(trustedPlainTemplate, "    executor: human\n  - id: build",
			"    executor: human\n    exit_gate_groups:\n      - - "+gate+"\n  - id: build", 1)
	}
	return map[string]string{
		"external gate": withGate(`gate: external_program
          version: 1
          params: {command: ["sh", "-c", "id"]}`),
		"docker gate with rw repo": withGate(`gate: docker_program
          version: 1
          params:
            image: ` + trustedImage + `
            command: ["/bin/sh", "-c", "id"]
            repo_access: rw`),
		"new executor adapter": trustedPlainTemplate + `executor_adapters:
  evil:
    argv: ["/bin/sh", "-c", "id"]
    timeout: 1m
    docker:
      image: ` + trustedImage + `
      network: none
`,
		"new project check": trustedPlainTemplate + `checks:
  - {name: evil, class: unit, command: [sh, -c, id], policy: required, timeout: 5m}
`,
		"new mcp server": trustedPlainTemplate + `mcp_servers:
  evil:
    command: /bin/sh
    args: ["-c", "id"]
`,
	}
}

func TestWebTemplateRoutesRefuseCommandsREST(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(controlDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(trustedPlainTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	version := config.TemplateVersionID([]byte(trustedPlainTemplate))
	for name, yaml := range webTemplateAttempts() {
		validate := authorizedRequest(t, srv, http.MethodPost, "/api/template/validate", `{"yaml":`+mustJSONTemplate(t, yaml)+`}`)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, validate)
		var verdict struct {
			Valid      bool   `json:"valid"`
			Code       string `json:"code"`
			Diagnostic string `json:"diagnostic"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &verdict); err != nil {
			t.Fatalf("%s: validate body: %v %s", name, err, response.Body.String())
		}
		if verdict.Valid || verdict.Code != config.ErrCodeTrustedConfigOnly || !strings.Contains(verdict.Diagnostic, ".ai-team/config.yaml") {
			t.Errorf("%s: validate must refuse with %s and point at the file config: %+v", name, config.ErrCodeTrustedConfigOnly, verdict)
		}

		publish := authorizedRequest(t, srv, http.MethodPost, "/api/template/publish",
			`{"yaml":`+mustJSONTemplate(t, yaml)+`,"expected_version":"`+version+`"}`)
		response = httptest.NewRecorder()
		srv.router.ServeHTTP(response, publish)
		var refusal struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil || response.Code != http.StatusBadRequest ||
			refusal.Code != config.ErrCodeTrustedConfigOnly || !strings.Contains(refusal.Error, "файловом конфиге") {
			t.Errorf("%s: publish must answer 400 %s: %d %s", name, config.ErrCodeTrustedConfigOnly, response.Code, response.Body.String())
		}
	}
	if data, _ := os.ReadFile(configPath); string(data) != trustedPlainTemplate {
		t.Fatal("a refused publication changed the active template")
	}
	// The same template without commands is still accepted.
	validate := authorizedRequest(t, srv, http.MethodPost, "/api/template/validate", `{"yaml":`+mustJSONTemplate(t, trustedPlainTemplate)+`}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, validate)
	if !strings.Contains(response.Body.String(), `"valid":true`) {
		t.Fatalf("a template without commands must validate: %s", response.Body.String())
	}
}

func TestWebTemplateRoutesRefuseCommandsMCP(t *testing.T) {
	env := newMCPActionEnv(t, nil, nil)
	configPath := filepath.Join(env.target, ".ai-team", "config.yaml")
	if err := os.WriteFile(configPath, []byte(trustedPlainTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := env.as(t, actorOwner)
	version := config.TemplateVersionID([]byte(trustedPlainTemplate))
	for name, yaml := range webTemplateAttempts() {
		text, failed := owner.call("validate_template", map[string]any{"yaml": yaml})
		if failed || !strings.Contains(text, `"valid":false`) || !strings.Contains(text, config.ErrCodeTrustedConfigOnly) {
			t.Errorf("%s: validate_template must report %s: %v %s", name, config.ErrCodeTrustedConfigOnly, failed, text)
		}
		failure := owner.refused("publish_template", map[string]any{"yaml": yaml, "expected_version": version})
		if failure.Status != http.StatusBadRequest || failure.Code != config.ErrCodeTrustedConfigOnly {
			t.Errorf("%s: publish_template must be refused with 400 %s: %+v", name, config.ErrCodeTrustedConfigOnly, failure)
		}
	}
}

// The file config keeps carrying commands: it loads and validates, the editor
// reads it, a name-only reference to its trusted adapter and check is accepted
// from the web, and republishing its command gates through the web is refused.
func TestFileConfigKeepsCommandsAndWebMayReferenceThemByName(t *testing.T) {
	cfg := mustParseForTest(t, trustedCommandsFileConfig)
	if err := cfg.Validate(nil); err != nil && !strings.Contains(err.Error(), "demo-reviewer") {
		t.Fatalf("the file config must accept command gates, adapters and checks: %v", err)
	}
	if problems := cfg.UntrustedCommandProblems(nil); len(problems) != 4 {
		t.Fatalf("with no trusted template everything command-bearing is flagged (2 gates, adapter, check): %v", problems)
	}
	if problems := cfg.UntrustedCommandProblems(cfg); len(problems) != 2 {
		t.Fatalf("only the two command gates stay refused against the same template: %v", problems)
	}

	env := newMCPActionEnv(t, nil, nil)
	configPath := filepath.Join(env.target, ".ai-team", "config.yaml")
	if err := os.WriteFile(configPath, []byte(trustedCommandsFileConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := env.as(t, actorOwner)
	if text, failed := owner.call("get_template", nil); failed || !strings.Contains(text, "host_check") {
		t.Fatalf("the editor must still show the file config: %v %s", failed, text)
	}
	if text, _ := owner.call("validate_template", map[string]any{"yaml": trustedCommandsFileConfig}); !strings.Contains(text, config.ErrCodeTrustedConfigOnly) {
		t.Fatalf("republishing the command gates through the web must be refused: %s", text)
	}
	// Without the gates the adapter and check stay referenced by name.
	noGates := strings.Replace(trustedCommandsFileConfig, trustedGates, "", 1)
	if problems := mustParseForTest(t, noGates).UntrustedCommandProblems(cfg); len(problems) != 0 {
		t.Fatalf("name-only references to trusted definitions must be accepted: %v", problems)
	}
	// A changed adapter command is refused even under the trusted name.
	changed := strings.Replace(noGates, `argv: ["/bin/sh", "/agent/review.sh"]`, `argv: ["/bin/sh", "-c", "id"]`, 1)
	if problems := mustParseForTest(t, changed).UntrustedCommandProblems(cfg); len(problems) != 1 || !strings.Contains(problems[0], "executor_adapters.box-review") {
		t.Fatalf("a changed trusted adapter must be refused: %v", problems)
	}
}

func mustParseForTest(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
