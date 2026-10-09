package runtime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// MCPServerConfig is the deliberately small stdio-only Codex MCP contract.
// Definitions are trusted project configuration; stages opt into named
// definitions individually. Remote transports and user/global config merging
// are intentionally not supported.
type MCPServerConfig struct {
	Name              string            `yaml:"-"`
	Command           string            `yaml:"command"`
	Args              []string          `yaml:"args,omitempty"`
	Env               map[string]string `yaml:"env,omitempty"`
	EnvVars           []string          `yaml:"env_vars,omitempty"`
	StartupTimeoutSec int               `yaml:"startup_timeout_sec,omitempty"`
	ToolTimeoutSec    int               `yaml:"tool_timeout_sec,omitempty"`
}

// UnmarshalYAML rejects transport, OAuth, and other Codex MCP fields outside
// the deliberately supported local stdio contract. yaml.v3 otherwise ignores
// unknown struct fields, which could make unsupported security settings look
// as if they had been applied.
func (server *MCPServerConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("mcp server definition must be a mapping")
	}
	allowed := map[string]bool{
		"command": true, "args": true, "env": true, "env_vars": true,
		"startup_timeout_sec": true, "tool_timeout_sec": true,
	}
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || !allowed[key.Value] {
			return fmt.Errorf("mcp_servers: unsupported key %q", key.Value)
		}
	}
	type plain MCPServerConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*server = MCPServerConfig(decoded)
	return nil
}

const (
	DefaultMCPStartupTimeoutSec = 10
	DefaultMCPToolTimeoutSec    = 60
	MaxMCPStartupTimeoutSec     = 60
	MaxMCPToolTimeoutSec        = 300
	MaxMCPServersPerStage       = 4
	MaxMCPArgsPerServer         = 32
	MaxMCPArgBytes              = 4096
	MaxMCPArgsBytes             = 16 * 1024
	MaxMCPEnvPerServer          = 32
	MaxMCPEnvValueBytes         = 4096
)

var (
	mcpServerIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	mcpEnvNamePattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	// Secret-like variable names are rejected in both literal env and env_vars.
	// MCP servers never receive provider/session credentials, even when someone
	// accidentally places one in an allowlist.
	mcpCredentialNamePattern = regexp.MustCompile(`(^|_)(API_KEY|TOKEN|SECRET|PASSWORD|CREDENTIALS?|AUTH|PRIVATE_KEY|PAT|ACCESS_KEY|CLIENT_SECRET)(_|$)`)
)

var mcpReservedEnvironmentNames = map[string]bool{
	"CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true, "HOME": true,
	"AI_TEAM_HARNESS_ENV_ALLOW": true, "AI_TEAM_OPENCODE_ENV_ALLOW": true,
}

// Validate checks an MCP server definition before any runtime configuration
// is written or process is launched. It accepts a command plus fixed argv only;
// no shell parsing, interpolation, or implicit PATH search is permitted.
func (server MCPServerConfig) Validate(id string) error {
	if !mcpServerIDPattern.MatchString(id) {
		return fmt.Errorf("mcp_servers: invalid server id %q", id)
	}
	if !filepath.IsAbs(server.Command) || strings.TrimSpace(server.Command) != server.Command || len(server.Command) > MaxMCPArgBytes || !utf8.ValidString(server.Command) || validateMCPString(server.Command, MaxMCPArgBytes) != nil {
		return fmt.Errorf("mcp_servers.%s.command must be an absolute executable path", id)
	}
	if isMCPCommandShell(server.Command) {
		return fmt.Errorf("mcp_servers.%s.command must not be a shell", id)
	}
	if len(server.Args) > MaxMCPArgsPerServer {
		return fmt.Errorf("mcp_servers.%s.args has more than %d arguments", id, MaxMCPArgsPerServer)
	}
	argBytes := 0
	for _, arg := range server.Args {
		if err := validateMCPString(arg, MaxMCPArgBytes); err != nil {
			return fmt.Errorf("mcp_servers.%s.args: %w", id, err)
		}
		argBytes += len(arg)
	}
	if argBytes > MaxMCPArgsBytes {
		return fmt.Errorf("mcp_servers.%s.args exceeds %d bytes total", id, MaxMCPArgsBytes)
	}
	if len(server.Env)+len(server.EnvVars) > MaxMCPEnvPerServer {
		return fmt.Errorf("mcp_servers.%s env/env_vars has more than %d entries", id, MaxMCPEnvPerServer)
	}
	for name, value := range server.Env {
		if err := validateMCPEnvName(name); err != nil {
			return fmt.Errorf("mcp_servers.%s.env: %w", id, err)
		}
		if err := validateMCPString(value, MaxMCPEnvValueBytes); err != nil {
			return fmt.Errorf("mcp_servers.%s.env.%s: %w", id, name, err)
		}
	}
	seenEnvVars := map[string]bool{}
	for _, name := range server.EnvVars {
		if err := validateMCPEnvName(name); err != nil {
			return fmt.Errorf("mcp_servers.%s.env_vars: %w", id, err)
		}
		if seenEnvVars[name] {
			return fmt.Errorf("mcp_servers.%s.env_vars contains duplicate %q", id, name)
		}
		if _, exists := server.Env[name]; exists {
			return fmt.Errorf("mcp_servers.%s defines %q in both env and env_vars", id, name)
		}
		seenEnvVars[name] = true
	}
	if server.StartupTimeoutSec < 0 || server.StartupTimeoutSec > MaxMCPStartupTimeoutSec {
		return fmt.Errorf("mcp_servers.%s.startup_timeout_sec must be between 1 and %d when set", id, MaxMCPStartupTimeoutSec)
	}
	if server.ToolTimeoutSec < 0 || server.ToolTimeoutSec > MaxMCPToolTimeoutSec {
		return fmt.Errorf("mcp_servers.%s.tool_timeout_sec must be between 1 and %d when set", id, MaxMCPToolTimeoutSec)
	}
	return nil
}

func validateMCPEnvName(name string) error {
	if !mcpEnvNamePattern.MatchString(name) {
		return fmt.Errorf("invalid environment variable name %q", name)
	}
	if mcpReservedEnvironmentNames[name] || (strings.HasPrefix(name, "AI_TEAM_") && strings.HasSuffix(name, "_ALLOW")) {
		return fmt.Errorf("environment variable %q is reserved for runtime isolation", name)
	}
	if mcpCredentialNamePattern.MatchString(name) {
		return fmt.Errorf("credential-like environment variable %q must never be passed to an MCP server", name)
	}
	return nil
}

func validateMCPString(value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value is not valid UTF-8")
	}
	if len(value) > maxBytes {
		return fmt.Errorf("value exceeds %d bytes", maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("control characters are not allowed")
		}
	}
	return nil
}

func isMCPCommandShell(command string) bool {
	switch strings.ToLower(filepath.Base(command)) {
	case "sh", "bash", "dash", "zsh", "fish", "ksh", "csh", "tcsh", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

func effectiveMCPStartupTimeout(server MCPServerConfig) int {
	if server.StartupTimeoutSec == 0 {
		return DefaultMCPStartupTimeoutSec
	}
	return server.StartupTimeoutSec
}

func effectiveMCPToolTimeout(server MCPServerConfig) int {
	if server.ToolTimeoutSec == 0 {
		return DefaultMCPToolTimeoutSec
	}
	return server.ToolTimeoutSec
}
