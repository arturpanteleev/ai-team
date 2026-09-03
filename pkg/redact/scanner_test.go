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

// TestRedactAlignsWithScanFilter — RedactFile применяет тот же
// likelySecretValue-фильтр к secret assignment, что и Scan: бенign-значение
// не режется (иначе scan и redact расходились бы по контракту P1-6).
func TestRedactAlignsWithScanFilter(t *testing.T) {
	input := []byte("password=0123456789abcdef\npassword=A1b2C3d4E5f6G7h8\n")
	redacted := string(RedactFile(input))
	if !strings.Contains(redacted, "password=0123456789abcdef") {
		t.Errorf("бенign-значение (без верхнего регистра) не должно резаться: %q", redacted)
	}
	if strings.Contains(redacted, "A1b2C3d4E5f6G7h8") {
		t.Errorf("высокоэнтропийное значение должно быть вырезано: %q", redacted)
	}
}

// TestRedactBlanksPrivateKeyBody — RedactFile вырезает ВЕСЬ PEM-блок private
// key (BEGIN … тело base64 … END), а не только BEGIN-строку.
func TestRedactBlanksPrivateKeyBody(t *testing.T) {
	header := "-----BEGIN RSA " + "PRIVATE KEY-----"
	footer := "-----END RSA " + "PRIVATE KEY-----"
	body := "MIIEpAIBAAKCAQEA" + strings.Repeat("A", 40)
	input := []byte("before\n" + header + "\n" + body + "\n" + footer + "\nafter\n")
	redacted := string(RedactFile(input))
	if strings.Contains(redacted, body) {
		t.Errorf("тело private key осталось в redacted-копии: %q", redacted)
	}
	if strings.Contains(redacted, "MIIE") {
		t.Errorf("BEGIN/тело блока не полностью вырезаны: %q", redacted)
	}
	if !strings.Contains(redacted, "before") || !strings.Contains(redacted, "after") {
		t.Errorf("контекст повреждён: %q", redacted)
	}
	if strings.Count(redacted, "[REDACTED:private key]") < 3 {
		t.Errorf("ожидались 3+ маркера (begin/body/end): %q", redacted)
	}
	if strings.Count(redacted, "\n") != strings.Count(string(input), "\n") {
		t.Errorf("число переводов строк не сохранилось: redacted=%q", redacted)
	}
	if strings.Contains(redacted, "]"+header) || strings.Contains(redacted, "]"+footer) {
		t.Errorf("несколько маркеров склеены в одну строку (потерян \\n): %q", redacted)
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
