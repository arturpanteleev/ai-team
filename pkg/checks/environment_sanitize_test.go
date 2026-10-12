package checks

import (
	"strings"
	"testing"
)

func TestSanitizedCheckEnvironmentDropsCredentials(t *testing.T) {
	got := sanitizedCheckEnvironment([]string{
		"PATH=/usr/bin",
		"GH_TOKEN=gh-secret",
		"GITHUB_TOKEN=github-secret",
		"OPENAI_API_KEY=openai-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"APP_PASSWORD=app-secret",
		"LANG=C.UTF-8",
	})
	joined := strings.Join(got, "\n")
	for _, secret := range []string{"GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "APP_PASSWORD"} {
		if strings.Contains(joined, secret) {
			t.Errorf("secret environment variable %s was retained: %q", secret, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "LANG=C.UTF-8") {
		t.Fatalf("non-secret build environment was not preserved: %q", joined)
	}
}
