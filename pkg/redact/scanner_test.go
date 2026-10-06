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
	findings := Scan(input).Findings
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

// TestScanIgnoresBenignText — отвергать эти строки обязан фильтр
// likelySecretValue, а не регистр ключа (#150: раньше кейсы проходили
// вхолостую, потому что `PASSWORD=` не матчился вовсе). Поэтому все значения
// здесь длиннее 16 символов — то есть дошли бы до фильтра, будь они похожи
// на секрет.
func TestScanIgnoresBenignText(t *testing.T) {
	input := []byte(`The password manager is configured.
value=some-value
count=12
PASSWORD=example-value-0123456789
PASSWORD=<from-vault-secret-0123>
TOKEN=your-token-here-0123456789
API_KEY=${CI_API_KEY_VARIABLE_0}
api_key = "changeme-0123456789abcdef"
SIGNING_KEY=placeholder0123456789
`)
	findings := Scan(input).Findings
	for _, f := range findings {
		if f.Reason == "secret assignment" {
			t.Errorf("ложное срабатывание secret assignment: %+v (line %d)", f, f.Line)
		}
	}
}

// TestScanDetectsUpperCaseAssignment — #150/QS-08: ключ в любом регистре.
// Каждый кейс обязан краснеть при снятом (?i) у альтернации ключей.
func TestScanDetectsUpperCaseAssignment(t *testing.T) {
	const secret = "Aa1bB2cC3dD4eE5fF6gG7"
	cases := []string{
		"PASSWORD=" + secret,
		"Password=" + secret,
		"API_KEY=" + secret,
		"Api-Key: " + secret,
		"SECRET = " + secret,
		"Token: " + secret,
		"CLIENT_SECRET=" + secret,
		"ACCESS_KEY=" + secret,
		"AUTH_TOKEN=" + secret,
		"SIGNING_KEY=" + secret,
		"PASSWD=" + secret,
		`  "PRIVATE_KEY": "` + secret + `"`,
	}
	for _, line := range cases {
		findings := Scan([]byte(line + "\n")).Findings
		if !hasFindingReason(findings, "secret assignment") {
			t.Errorf("секрет не обнаружен в %q: %+v", line, findings)
		}
		if redacted := string(RedactFile([]byte(line))); strings.Contains(redacted, secret) {
			t.Errorf("секрет уцелел в redacted-копии %q: %q", line, redacted)
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
// F-7: значение с буквой и цифрой (в т.ч. lowercase-hex/base64) — секрет;
// чистое слово без цифр — benign.
func TestRedactAlignsWithScanFilter(t *testing.T) {
	input := []byte("password=thequickbrownfoxjumpsover\npassword=0123456789abcdef\npassword=A1b2C3d4E5f6G7h8\n")
	redacted := string(RedactFile(input))
	if !strings.Contains(redacted, "password=thequickbrownfoxjumpsover") {
		t.Errorf("чистое слово без цифр не должно резаться: %q", redacted)
	}
	if strings.Contains(redacted, "0123456789abcdef") {
		t.Errorf("lowercase-hex токен (буквы+цифры) должен быть вырезан: %q", redacted)
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

func hasFindingReason(findings []Finding, reason string) bool {
	for _, f := range findings {
		if f.Reason == reason {
			return true
		}
	}
	return false
}

// AUD-03: секретные JSON-поля обнаруживаются структурно независимо от того,
// в одну ли строку записан документ.
func TestScanJSONSecretFields(t *testing.T) {
	secret := "s3cReTValX9zW8qK2nM4pR7t"
	input := []byte(`{
  "service": {
    "name": "billing",
    "credentials": {
      "password": "` + secret + `"
    },
    "client_secret": "` + secret + `",
    "owner": "team-core"
  }
}`)
	findings := Scan(input).Findings
	if !hasFindingReason(findings, jsonSecretReason) {
		t.Fatalf("json secret field не обнаружен: %+v", findings)
	}
	for _, f := range findings {
		if f.Reason == jsonSecretReason && f.Redacted != jsonSecretRedacted {
			t.Fatalf("неверный redaction-маркер: %+v", f)
		}
	}
}

// AUD-03: плейсхолдеры/известные benign-значения в JSON-полях секретным
// evidence НЕ являются (тот же likelySecretValue-фильтр, что у assignment).
func TestScanJSONIgnoresPlaceholders(t *testing.T) {
	input := []byte(`{"password": "your-password", "token": "changeme", "api_key": "placeholder"}`)
	findings := Scan(input).Findings
	if hasFindingReason(findings, jsonSecretReason) {
		t.Fatalf("placeholder-значение посчитано секретом: %+v", findings)
	}
}

// F-7: секретный контекст не теряется на массивах и во вложенных значениях —
// value-секреты в массивах секретных полей обнаруживаются, как и скалярные.
func TestScanJSONSecretArrayContext(t *testing.T) {
	secretA := "arraySecretV1x9Zq7kL4m8r"
	secretB := "nestedValueQ2w8eR6t3y"
	secretC := "deepTokenA5s9Df3gH"
	input := []byte(`{
  "issue": {
    "tokens": ["` + secretA + `"],
    "client_secret": [{"token": "` + secretB + `"}]
  },
  "api_key": ["` + secretC + `"]
}`)
	findings := Scan(input).Findings
	var jsonSec []string
	for _, f := range findings {
		if f.Reason == jsonSecretReason {
			jsonSec = append(jsonSec, f.Matched)
		}
	}
	for _, want := range []string{secretA, secretB, secretC} {
		found := false
		for _, m := range jsonSec {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("секрет %q внутри массива не обнаружен: %+v", want, jsonSec)
		}
	}
}

// F-7: теперь секретимы и lowercase"буквой+цифрой" токены (раньше требовался
// верхний регистр): hex/base64-подобные токены в секретных полях режутся.
func TestScanJSONDetectsLowerHexToken(t *testing.T) {
	input := []byte(`{"api_key": "0123456789abcdef0123456789abcdef"}`)
	findings := Scan(input).Findings
	if !hasFindingReason(findings, jsonSecretReason) {
		t.Fatalf("lower-hex токен в секретном поле не обнаружен: %+v", findings)
	}
	for _, f := range findings {
		if f.Reason == jsonSecretReason && f.Redacted != jsonSecretRedacted {
			t.Fatalf("неверный redaction-маркер: %+v", f)
		}
	}
}

// AUD-03: один и тот же синтетический секрет обнаруживается в plain-тексте,
// JSON, JSONL и nested JSON с корректной привязкой к строке.
func TestScanDetectsSecretAcrossFormats(t *testing.T) {
	secret := "T0pSecretValue21k9XzW8qK2nM4"
	cases := []struct {
		name string
		data string
		line int
	}{
		{"plain", "api_key = " + secret + "\n", 1},
		{"json", "{\"api_key\":\"" + secret + "\"}\n", 1},
		{"nested json", "{\"k8s\":{\"deploy\":{\"token\":\"" + secret + "\"}}}\n", 1},
		{"jsonl", "{\"stage\":1}\n{\"credentials\":{\"password\":\"" + secret + "\"},\"ok\":1}\n", 2},
	}
	for _, tc := range cases {
		findings := Scan([]byte(tc.data)).Findings
		if len(findings) == 0 {
			t.Errorf("%s: секрет не обнаружен: %+v", tc.name, findings)
			continue
		}
		var jsonFound bool
		for _, f := range findings {
			if f.Reason == jsonSecretReason {
				jsonFound = true
				if f.Line != tc.line {
					t.Errorf("%s: line=%d, want %d (%+v)", tc.name, f.Line, tc.line, f)
				}
			}
		}
		if tc.name == "plain" {
			continue // plain покрывается assignment-правилом, не JSON-им
		}
		if !jsonFound {
			t.Errorf("%s: json secret field не обнаружен: %+v", tc.name, findings)
		}
	}
}

// AUD-03: не-JSON содержимое не даёт ложного json-finding и не порождает
// ложного «не просканировано» — plain-сканер остаётся источником evidence.
func TestScanJSONSkipsNonJSON(t *testing.T) {
	if hasFindingReason(Scan([]byte("just text password = placeholder\n")).Findings, jsonSecretReason) {
		t.Fatal("обычный текст не должен давать json finding")
	}
	if hasFindingReason(Scan([]byte("import x; x = 1\n")).Findings, jsonSecretReason) {
		t.Fatal("встроенные строки без JSON-структуры не должны давать json finding")
	}
}

// nestJSON собирает документ глубины depth: {"a":{"a":…{"a":inner}…}}.
func nestJSON(depth int, inner string) string {
	doc := inner
	for i := 0; i < depth; i++ {
		doc = `{"a":` + doc + `}`
	}
	return doc
}

// QS-08 (дефект 2): документ крупнее прежнего лимита 4 MiB сканируется
// структурно. Раньше тот же секрет давал findings=1 в 36-байтном документе
// и findings=0 в 4-мегабайтном.
func TestScanJSONBeyondOldSizeLimit(t *testing.T) {
	const secret = "Aa1bB2cC3dD4eE5fF6gG7"
	small := []byte(`{"password":"` + secret + `"}`)
	if !hasFindingReason(Scan(small).Findings, jsonSecretReason) {
		t.Fatal("секрет не найден в малом документе")
	}
	big := []byte(`{"filler":"` + strings.Repeat("x", 4<<20) + `","password":"` + secret + `"}`)
	result := Scan(big)
	if !hasFindingReason(result.Findings, jsonSecretReason) {
		t.Errorf("документ %d B: секрет не найден (прежний лимит 4 MiB)", len(big))
	}
	if !result.Complete() {
		t.Errorf("документ в пределах файлового лимита не должен давать пропусков: %+v", result.Unscanned)
	}
}

// QS-08 (дефект 3): вложенность глубже лимита пропускает ПОДДЕРЕВО, а не
// документ. Секрет на верхнем уровне после глубокого объекта обязан
// находиться при любой глубине.
func TestScanJSONDepthSkipsSubtreeNotDocument(t *testing.T) {
	const secret = "Aa1bB2cC3dD4eE5fF6gG7"
	for _, depth := range []int{5, 15, 16, 17, 20, 64} {
		doc := `{"deep":` + nestJSON(depth, `"leaf"`) + `,"password":"` + secret + `"}`
		result := Scan([]byte(doc))
		if !hasFindingReason(result.Findings, jsonSecretReason) {
			t.Errorf("depth=%d: секрет после глубокого поддерева не найден: %+v", depth, result)
		}
		if depth < maxJSONDepth && !result.Complete() {
			t.Errorf("depth=%d: пропусков быть не должно: %+v", depth, result.Unscanned)
		}
	}
}

// QS-08: пропущенное поддерево обязано попасть в отчёт как «не
// просканировано» — молчаливый пропуск хуже отсутствующего механизма.
func TestScanJSONDepthReportsUnscanned(t *testing.T) {
	const secret = "Aa1bB2cC3dD4eE5fF6gG7"
	doc := nestJSON(maxJSONDepth+4, `{"password":"`+secret+`"}`)
	result := Scan([]byte(doc))
	if result.Complete() {
		t.Fatalf("секрет глубже лимита пропущен молча: %+v", result)
	}
	if hasFindingReason(result.Findings, jsonSecretReason) {
		t.Fatalf("поддерево глубже лимита не должно сканироваться: %+v", result.Findings)
	}
	var depthGap bool
	for _, u := range result.Unscanned {
		if u.Reason != unscannedJSONDepth {
			continue
		}
		depthGap = true
		if u.Line != 1 {
			t.Errorf("строка пропуска = %d, want 1: %+v", u.Line, u)
		}
		if u.Detail == "" {
			t.Errorf("пропуск без объяснения: %+v", u)
		}
	}
	if !depthGap {
		t.Errorf("нет записи %q в Unscanned: %+v", unscannedJSONDepth, result.Unscanned)
	}
}

// QS-08: после пропуска глубокого поддерева обход продолжается — секреты
// на соседних ветках того же уровня находятся.
func TestScanJSONContinuesAfterDeepSubtree(t *testing.T) {
	const secret = "Aa1bB2cC3dD4eE5fF6gG7"
	doc := `{"a":` + nestJSON(maxJSONDepth+2, `[1,2,{"x":"y"}]`) +
		`,"b":[` + nestJSON(maxJSONDepth+2, `"z"`) + `],"token":"` + secret + `"}`
	result := Scan([]byte(doc))
	if !hasFindingReason(result.Findings, jsonSecretReason) {
		t.Fatalf("секрет после двух глубоких поддеревьев не найден: %+v", result)
	}
	if len(result.Unscanned) == 0 {
		t.Fatalf("пропущенные поддеревья не попали в отчёт: %+v", result)
	}
}

// QS-08: оборванный JSON внутри контейнера — тоже «не просканировано»:
// документ начинался как JSON, остаток не разобран.
func TestScanJSONTruncatedReportsUnscanned(t *testing.T) {
	result := Scan([]byte(`{"a":{"b":1},"c":[1,2`))
	if result.Complete() {
		t.Fatalf("оборванный JSON должен фиксироваться как непросканированный: %+v", result)
	}
	if result.Unscanned[0].Reason != unscannedJSONParse {
		t.Fatalf("причина = %q, want %q", result.Unscanned[0].Reason, unscannedJSONParse)
	}
}

// QS-08: обычный текст, код и markdown НЕ должны порождать ложных записей
// «не просканировано» — иначе fail-closed вердикт утонет в шуме.
func TestScanNonJSONHasNoUnscannedNoise(t *testing.T) {
	cases := []string{
		"2026-09-21 10:00:00 INFO stage=review verdict=APPROVED\n",
		"package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"# Заголовок\n\n- пункт: значение\n- ещё: 42\n",
		"body { color: red; }\n",
		"key: value\nother: 12\n",
		"",
	}
	for _, data := range cases {
		if result := Scan([]byte(data)); !result.Complete() {
			t.Errorf("ложное «не просканировано» на %q: %+v", data, result.Unscanned)
		}
	}
}

// QS-08: номера строк остаются верными на многострочном документе — после
// перехода на инкрементальный lineTracker.
func TestScanJSONLineNumbersRemainCorrect(t *testing.T) {
	secret := "T0pSecretValue21k9XzW8qK2nM4"
	doc := "{\n  \"a\": 1,\n  \"b\": {\n    \"token\": \"" + secret + "\"\n  },\n  \"password\": \"" + secret + "\"\n}\n"
	lines := map[int]bool{}
	for _, f := range Scan([]byte(doc)).Findings {
		if f.Reason == jsonSecretReason {
			lines[f.Line] = true
		}
	}
	for _, want := range []int{4, 6} {
		if !lines[want] {
			t.Errorf("нет json-находки на строке %d: %v", want, lines)
		}
	}
}
