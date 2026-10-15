package gaterun

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/redact"
)

// Defaults and hard caps of the external gate parameters.
const (
	DefaultTimeout        = 60 * time.Second
	MaxTimeout            = time.Hour
	DefaultMaxOutputBytes = int64(1 << 20)
	MaxOutputBytesCap     = int64(16 << 20)
)

// Config is the validated form of the external_program gate parameters. It is
// read only from the trusted template/repository configuration; nothing in it
// can come from the UI.
type Config struct {
	// Command is the argv, run without a shell. The program is an absolute
	// path, a path relative to the repository root (contains a slash), or a
	// bare name looked up in the controller's PATH.
	Command []string
	Timeout time.Duration
	// CWD is the working directory relative to the repository root ("." by
	// default).
	CWD string
	// EnvAllow names the only host variables the program inherits.
	EnvAllow       []string
	MaxOutputBytes int64
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ParseParams validates the parameters of an external_program gate reference
// against the library schema and the deeper rules the schema cannot express
// (safe cwd, env names, no inline secrets in argv, caps).
func ParseParams(params map[string]any) (Config, error) {
	def, err := gatelib.Default().Lookup(gatelib.GateExternalProgram, 1)
	if err != nil {
		return Config{}, err
	}
	if err := gatelib.ValidateParams(def, params); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Command:        gatelib.StringListParam(params, "command"),
		Timeout:        DefaultTimeout,
		CWD:            ".",
		EnvAllow:       gatelib.StringListParam(params, "env_allow"),
		MaxOutputBytes: DefaultMaxOutputBytes,
	}
	if _, ok := params["timeout_seconds"]; ok {
		cfg.Timeout = time.Duration(gatelib.IntParam(params, "timeout_seconds")) * time.Second
	}
	if _, ok := params["max_output_bytes"]; ok {
		cfg.MaxOutputBytes = int64(gatelib.IntParam(params, "max_output_bytes"))
	}
	if value := gatelib.StringParam(params, "cwd"); value != "" {
		cfg.CWD = value
	}
	return cfg, cfg.Validate()
}

// Validate enforces the rules shared by every constructor of Config.
func (c Config) Validate() error {
	if len(c.Command) == 0 || c.Command[0] == "" {
		return fmt.Errorf("external gate: command must be a non-empty argv")
	}
	for i, arg := range c.Command {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("external gate: command[%d] contains a NUL byte", i)
		}
		if containsSecret(arg) {
			return fmt.Errorf("external gate: command[%d] looks like an inline secret; pass secrets through env_allow", i)
		}
	}
	if c.Timeout <= 0 || c.Timeout > MaxTimeout {
		return fmt.Errorf("external gate: timeout_seconds must be between 1 and %d", int(MaxTimeout/time.Second))
	}
	if c.MaxOutputBytes <= 0 || c.MaxOutputBytes > MaxOutputBytesCap {
		return fmt.Errorf("external gate: max_output_bytes must be between 1 and %d", MaxOutputBytesCap)
	}
	if err := validRelDir(c.CWD); err != nil {
		return fmt.Errorf("external gate: cwd %q: %w", c.CWD, err)
	}
	if first := c.Command[0]; strings.Contains(first, "/") && !filepath.IsAbs(first) {
		if cleaned := path.Clean(first); cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("external gate: command[0] %q must stay inside the repository", first)
		}
	}
	for _, name := range c.EnvAllow {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("external gate: env_allow name %q is not a valid variable name", name)
		}
		if strings.HasPrefix(strings.ToUpper(name), "AI_TEAM_GATE_") {
			return fmt.Errorf("external gate: env_allow name %q is reserved", name)
		}
	}
	return nil
}

func validRelDir(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("must be a relative slash-separated path inside the repository")
	}
	if path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("must be clean and stay inside the repository")
	}
	return nil
}

// containsSecret reports whether an argv element embeds a credential.
func containsSecret(arg string) bool {
	candidates := []string{arg, strings.TrimLeft(arg, "-")}
	if _, value, ok := strings.Cut(arg, "="); ok {
		candidates = append(candidates, value)
	}
	for _, candidate := range candidates {
		if len(redact.Scan([]byte(candidate)).Findings) > 0 {
			return true
		}
	}
	return false
}
