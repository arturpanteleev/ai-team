package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMCPServerConfigRejectsUnsafeCommandsAndInputs(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
	}{
		{name: "relative command", server: MCPServerConfig{Name: "docs", Command: "./mcp-server"}},
		{name: "shell command", server: MCPServerConfig{Name: "docs", Command: "/bin/sh", Args: []string{"-c", "server"}}},
		{name: "credential literal env", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", Env: map[string]string{"GITHUB_TOKEN": "secret"}}},
		{name: "credential passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"OPENAI_API_KEY"}}},
		{name: "anthropic credential passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"ANTHROPIC_API_KEY"}}},
		{name: "codex credential passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"CODEX_API_KEY"}}},
		{name: "claude oauth credential passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}},
		{name: "harness web token passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"AI_TEAM_WEB_TOKEN"}}},
		{name: "github token passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"GH_TOKEN"}}},
		{name: "github token alias passthrough", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"GITHUB_TOKEN"}}},
		{name: "reserved runtime env", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", EnvVars: []string{"CODEX_HOME"}}},
		{name: "control char arg", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", Args: []string{"bad\narg"}}},
		{name: "long tool timeout", server: MCPServerConfig{Name: "docs", Command: "/usr/bin/server", ToolTimeoutSec: MaxMCPToolTimeoutSec + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.server.Validate("docs"); err == nil {
				t.Fatal("unsafe definition must be rejected")
			}
		})
	}
}

func TestMCPServerConfigRejectsShellExecutableSuffixesAcrossPlatforms(t *testing.T) {
	for _, executable := range []string{
		"sh.exe", "bash.exe", "dash.exe", "zsh.exe", "fish.exe",
		"ksh.exe", "csh.exe", "tcsh.exe", "BASH.EXE",
	} {
		t.Run(executable, func(t *testing.T) {
			server := MCPServerConfig{Command: filepath.Join(t.TempDir(), executable)}
			if err := server.Validate("shell"); err == nil {
				t.Fatalf("shell executable %q must be rejected", server.Command)
			}
		})
	}
}

func TestMCPServerYAMLRejectsUnsupportedTransportsAndFields(t *testing.T) {
	for _, input := range []string{
		"command: /usr/bin/server\nurl: https://example.test/mcp\n",
		"command: /usr/bin/server\ntransport: streamable_http\n",
	} {
		var server MCPServerConfig
		if err := yaml.Unmarshal([]byte(input), &server); err == nil {
			t.Fatalf("unsupported MCP definition field was silently accepted: %q", input)
		}
	}
}

func TestMCPServerConfigBoundsAndAcceptsExplicitNonSecretEnv(t *testing.T) {
	server := MCPServerConfig{
		Name:              "monitoring",
		Command:           "/opt/ai-team/bin/monitoring-mcp",
		Args:              []string{"--readonly", "--workspace", "production"},
		Env:               map[string]string{"LOG_LEVEL": "warn"},
		EnvVars:           []string{"MONITORING_ENDPOINT"},
		StartupTimeoutSec: 20,
		ToolTimeoutSec:    120,
	}
	if err := server.Validate("monitoring"); err != nil {
		t.Fatalf("valid read-only server definition rejected: %v", err)
	}
	if err := server.Validate("bad.id"); err == nil || !strings.Contains(err.Error(), "server id") {
		t.Fatalf("invalid server ID must fail closed, got %v", err)
	}
}
