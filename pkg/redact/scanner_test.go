package redact

import (
	"strings"
	"testing"
)

func TestScanDetectsCommonSecrets(t *testing.T) {
	ghToken := "ghp_" + strings.Repeat("1", 36)
	slackToken := "xoxb-" + strings.Repeat("9", 22)
	openaiKey := "sk-proj-" + strings.Repeat("a", 24)
	pemHeader := "-----BEGIN RSA " + "PRIVATE KEY-----"
	pemFooter := "-----END RSA " + "PRIVATE KEY-----"
	input := []byte(`GITHUB_TOKEN=` + ghToken + `
aws_access_key_id="AKIAIOSFODNN7EXAMPLE"
url=https://user:sekret@example.com/path
secret = yV3sM3Yl0n9WxQ2aB1cD4eF6gH8jK0lM2nP4qR6sT8
` + pemHeader + `
MIIEpAIBAAKCAQEA
` + pemFooter + `
` + openaiKey + `
` + slackToken + `
`)
	findings := Scan(input)
	if len(findings) == 0 {
		t.Fatal("секреты не обнаружены")
	}
	reasons := map[string]bool{}
	for _, f := range findings {
		reasons[f.Reason] = true
	}
	for _, want := range []string{"private key", "aws access key", "github token",
		"openai key", "slack token", "basic auth url", "secret assignment"} {
		if !reasons[want] {
			t.Errorf("не найдено правило %q", want)
		}
	}
}

func TestScanIgnoresBenignText(t *testing.T) {
	input := []byte(`The password manager is configured.
value=some-value
count=12
# PASSWORD=example
PASSWORD=<from-vault>
TOKEN=your-token-here
api_key = "changeme"
`)
	findings := Scan(input)
	for _, f := range findings {
		if f.Reason == "secret assignment" {
			t.Errorf("ложное срабатывание secret assignment: %+v (line %d)", f, f.Line)
		}
	}
}

func TestRedactReplacesFindings(t *testing.T) {
	ghToken := "ghp_" + strings.Repeat("7", 36)
	input := []byte("token=" + ghToken + "\nkeep normal text\n")
	redacted := RedactFile(input)
	if strings.Contains(string(redacted), "ghp_") {
		t.Fatalf("секрет не вырезан: %s", redacted)
	}
	if !strings.Contains(string(redacted), "[REDACTED:github token]") {
		t.Fatalf("нет маркера redaction: %s", redacted)
	}
	if !strings.Contains(string(redacted), "keep normal text") {
		t.Fatalf("обычный текст повреждён: %s", redacted)
	}
}

func TestClassifyField(t *testing.T) {
	for name, want := range map[string]FieldClass{
		"run_id":           FieldPublic,
		"password":         FieldSecret,
		"db_password":      FieldSecret,
		"access_key":       FieldSecret,
		"client_secret":    FieldSecret,
		"apiToken":         FieldSecret,
		"commit_sha":       FieldPublic,
		"signing_key_path": FieldSecret,
	} {
		if got := ClassifyField(name); got != want {
			t.Errorf("ClassifyField(%q) = %s, want %s", name, got, want)
		}
	}
}
