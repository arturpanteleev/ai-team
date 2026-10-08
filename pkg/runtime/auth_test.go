package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSubscriptionTokenIsDetectedAndPassed(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-oauth-token")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(HarnessEnvAllowVar, "")
	t.Setenv(HarnessEnvAllowLegacyVar, "")
	if got := DetectAuthentication("claude"); got != AuthenticationSubscription {
		t.Fatalf("DetectAuthentication(claude) = %q, want subscription", got)
	}
}

func TestClaudeOAuthTokenStaysClaudeOnlyWithGeneralAllowList(t *testing.T) {
	const otherAllowedKey = "AI_TEAM_TEST_EXPLICITLY_ALLOWED"
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-oauth-token")
	t.Setenv(otherAllowedKey, "allowed-value")
	t.Setenv(HarnessEnvAllowVar, "CLAUDE_CODE_OAUTH_TOKEN,"+otherAllowedKey)
	t.Setenv(HarnessEnvAllowLegacyVar, "")

	t.Run("codex", func(t *testing.T) {
		target := t.TempDir()
		env, cleanup, err := (&CodexAdapter{}).Environment(&Agent{Name: "coder"}, &Task{TargetDir: target})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if containsEnvironmentKey(env, "CLAUDE_CODE_OAUTH_TOKEN") {
			t.Fatal("Claude subscription token must not reach Codex even when named in the general allow-list")
		}
		if got := environmentValue(env, otherAllowedKey); got != "allowed-value" {
			t.Fatalf("other explicitly allowed key = %q, want %q", got, "allowed-value")
		}
	})

	t.Run("opencode", func(t *testing.T) {
		target := t.TempDir()
		env, cleanup, err := (&OpenCodeAdapter{}).Environment(&Agent{Name: "analyst", Mutation: "none"}, &Task{
			TargetDir: target, ArtifactRoot: filepath.Join(target, ".ai-team", "artifacts"), Feature: "feature",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if containsEnvironmentKey(env, "CLAUDE_CODE_OAUTH_TOKEN") {
			t.Fatal("Claude subscription token must not reach OpenCode even when named in the general allow-list")
		}
		if got := environmentValue(env, otherAllowedKey); got != "allowed-value" {
			t.Fatalf("other explicitly allowed key = %q, want %q", got, "allowed-value")
		}
	})

	t.Run("claude", func(t *testing.T) {
		env, cleanup, err := (&ClaudeAdapter{}).Environment(&Agent{Name: "coder"}, &Task{TargetDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if got := environmentValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); got != "synthetic-oauth-token" {
			t.Fatalf("Claude subscription token = %q, want it passed to Claude", got)
		}
		if got := environmentValue(env, otherAllowedKey); got != "allowed-value" {
			t.Fatalf("other explicitly allowed key = %q, want %q", got, "allowed-value")
		}
	})
}

func TestClaudeAPIKeyRequiresExplicitAllowList(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-api-key")
	t.Setenv(HarnessEnvAllowVar, "")
	t.Setenv(HarnessEnvAllowLegacyVar, "")
	if got := DetectAuthentication("claude"); got != AuthenticationNotFound {
		t.Fatalf("unallowed API key must not count as available auth, got %q", got)
	}
	t.Setenv(HarnessEnvAllowVar, "ANTHROPIC_API_KEY")
	if got := DetectAuthentication("claude"); got != AuthenticationAPIKey {
		t.Fatalf("allowed API key must be detected, got %q", got)
	}
}

func TestCodexAuthenticationUsesAuthModeWithoutExposingContents(t *testing.T) {
	home := setCodexAuthTestEnv(t)
	writeCodexAuthFixture(t, home, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`)
	if got := DetectAuthentication("codex"); got != AuthenticationSubscription {
		t.Fatalf("DetectAuthentication(codex) = %q, want subscription", got)
	}
}

func TestCodexEnvironmentCopiesAuthWithPrivatePermissions(t *testing.T) {
	home := setCodexAuthTestEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-oauth-token")
	fixture := `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`
	source := writeCodexAuthFixture(t, home, fixture)

	env, cleanup, err := (&CodexAdapter{}).Environment(&Agent{Name: "coder"}, &Task{TargetDir: t.TempDir()})
	if err != nil {
		t.Fatal("Codex Environment failed with a valid subscription auth file")
	}
	defer cleanup()
	if !containsEnvironmentKey(env, "CODEX_HOME") {
		t.Fatal("CODEX_HOME must be set")
	}
	if containsEnvironmentKey(env, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("Claude subscription token must not be passed to the Codex runtime")
	}
	codexHome := environmentValue(env, "CODEX_HOME")
	info, err := os.Stat(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatal("subscription auth file was not copied")
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("copied auth.json permissions = %04o, want 0600", got)
	}
	if info, err := os.Stat(codexHome); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("temporary CODEX_HOME permissions must be 0700: err=%v", err)
	}
	if info, err := os.Stat(source); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("source auth fixture unexpectedly changed: err=%v", err)
	}
}

func TestCodexEnvironmentPrefersAllowedAPIKeyToStoredAuth(t *testing.T) {
	home := setCodexAuthTestEnv(t)
	writeCodexAuthFixture(t, home, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access"}}`)
	t.Setenv("CODEX_API_KEY", "synthetic-api-key")
	t.Setenv(HarnessEnvAllowVar, "CODEX_API_KEY")

	env, cleanup, err := (&CodexAdapter{}).Environment(&Agent{Name: "coder"}, &Task{TargetDir: t.TempDir()})
	if err != nil {
		t.Fatal("Codex Environment failed with an explicitly allowed API key")
	}
	defer cleanup()
	if !containsEnvironmentKey(env, "CODEX_API_KEY") {
		t.Fatal("explicitly allowed CODEX_API_KEY was not passed to the runtime")
	}
	if _, err := os.Stat(filepath.Join(environmentValue(env, "CODEX_HOME"), "auth.json")); !os.IsNotExist(err) {
		t.Fatal("stored subscription auth must not be copied when an allowed API key is used")
	}
}

func TestCodexEnvironmentRejectsSymlinkedAuthFile(t *testing.T) {
	home := setCodexAuthTestEnv(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "outside-auth.json")
	if err := os.WriteFile(outside, []byte(`{"auth_mode":"chatgpt"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(codexDir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := (&CodexAdapter{}).Environment(&Agent{Name: "coder"}, &Task{TargetDir: t.TempDir()}); err == nil {
		cleanup()
		t.Fatal("symlinked auth.json must fail closed")
	}
}

func setCodexAuthTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv(HarnessEnvAllowVar, "")
	t.Setenv(HarnessEnvAllowLegacyVar, "")
	return home
}

func writeCodexAuthFixture(t *testing.T, home, contents string) string {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsEnvironmentKey(environment []string, name string) bool {
	for _, entry := range environment {
		if strings.HasPrefix(entry, name+"=") {
			return true
		}
	}
	return false
}
