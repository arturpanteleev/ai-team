package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// AuthenticationMethod is the locally detectable login path for a CLI
// runtime. It reports only the method, never credential material.
type AuthenticationMethod string

const (
	AuthenticationNotFound     AuthenticationMethod = "not_found"
	AuthenticationAPIKey       AuthenticationMethod = "api_key"
	AuthenticationSubscription AuthenticationMethod = "subscription"
)

// DetectAuthentication reports whether the selected runtime can receive an
// explicitly allowed API key or a supported subscription login. It does not
// validate credentials with the provider.
func DetectAuthentication(cli string) AuthenticationMethod {
	switch filepath.Base(strings.TrimSpace(cli)) {
	case "claude":
		if hasAllowedNonEmptyEnvironment("ANTHROPIC_API_KEY") {
			return AuthenticationAPIKey
		}
		if strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")) != "" {
			return AuthenticationSubscription
		}
	case "codex":
		if hasAllowedNonEmptyEnvironment("CODEX_API_KEY", "OPENAI_API_KEY") {
			return AuthenticationAPIKey
		}
		if auth, err := loadCodexAuthFile(); err == nil {
			return auth.method
		}
	}
	return AuthenticationNotFound
}

func hasAllowedNonEmptyEnvironment(names ...string) bool {
	allowed := allowedEnvironmentKeys()
	for _, name := range names {
		if !allowed[name] {
			continue
		}
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

type codexAuthFile struct {
	contents []byte
	method   AuthenticationMethod
}

// loadCodexAuthFile reads the user's Codex credentials without following a
// final symlink and classifies only the public auth_mode field. The file body
// stays in memory for the isolated CODEX_HOME copy and is never logged.
func loadCodexAuthFile() (*codexAuthFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("codex: не удалось определить домашний каталог")
	}
	path := filepath.Join(home, ".codex", "auth.json")
	contents, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	if !json.Valid(contents) {
		return nil, fmt.Errorf("codex: auth.json содержит некорректный JSON")
	}
	var metadata struct {
		AuthMode     string                     `json:"auth_mode"`
		OpenAIAPIKey *string                    `json:"OPENAI_API_KEY"`
		Tokens       map[string]json.RawMessage `json:"tokens"`
	}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return nil, fmt.Errorf("codex: auth.json содержит некорректный JSON")
	}
	method := AuthenticationNotFound
	switch strings.ToLower(metadata.AuthMode) {
	case "apikey":
		method = AuthenticationAPIKey
	case "chatgpt", "chatgptauthtokens":
		method = AuthenticationSubscription
	default:
		if metadata.OpenAIAPIKey != nil && strings.TrimSpace(*metadata.OpenAIAPIKey) != "" {
			method = AuthenticationAPIKey
		} else if hasCodexToken(metadata.Tokens) {
			method = AuthenticationSubscription
		}
	}
	return &codexAuthFile{contents: contents, method: method}, nil
}

func hasCodexToken(tokens map[string]json.RawMessage) bool {
	for _, name := range []string{"access_token", "refresh_token"} {
		value := strings.TrimSpace(string(tokens[name]))
		if value != "" && value != "null" && value != `""` {
			return true
		}
	}
	return false
}
