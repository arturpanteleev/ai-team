package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyAppliesIncludeExclude(t *testing.T) {
	p := Policy{
		Include: []string{"runs/**"},
		Exclude: []string{"**/reports/**"},
	}
	if !p.Applies("runs/abc/events.jsonl") {
		t.Error("runs/** должно включать events.jsonl")
	}
	if p.Applies("reports/foo.html") {
		t.Error("вне include должен быть исключён")
	}
	if p.Applies("runs/abc/reports/x.html") {
		t.Error("exclude **/reports/** должен исключать")
	}
}

func TestPolicyValidateRejectsTraversal(t *testing.T) {
	p := Policy{Include: []string{"../escape"}}
	if err := p.Validate(); err == nil {
		t.Error("glob с выходом из workspace должен быть отвергнут")
	}
}

func TestVerifyFailClosedOnSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("2", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := Policy{FailOnSecrets: true}
	report, err := Verify(dir, p)
	if err == nil {
		t.Fatalf("fail-closed: ожидалась ошибка, report=%+v", report)
	}
	if report == nil || len(report.Violations) != 1 {
		t.Fatalf("ожидалась 1 violation, got %+v", report)
	}
	if report.Violations[0].Path != "runs/x/events.jsonl" {
		t.Fatalf("путь нарушения: %s", report.Violations[0].Path)
	}
}

func TestVerifyCleanPasses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"),
		[]byte("{\"stage\":\"review\",\"verdict\":\"APPROVED\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: true})
	if err != nil {
		t.Fatalf("чистый каталог не должен фейлить: %v", err)
	}
	if report.Verdict != "clean" {
		t.Fatalf("verdict = %s", report.Verdict)
	}
}

func TestVerifyExcludeSkipsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "tokens.txt"),
		[]byte("GITHUB_TOKEN="+"ghp_"+strings.Repeat("3", 36)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := Policy{FailOnSecrets: true, Exclude: []string{"meta/**"}}
	if _, err := Verify(dir, p); err != nil {
		t.Fatalf("excluded-файл должен быть проигнорирован: %v", err)
	}
}

// AUD-03: секрет в JSON/JSONL поле блокирует export (fail-closed) так же, как
// plain-текст.
func TestVerifyBlocksJSONSecretFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secret := "T0pSecretValue21k9XzW8qK2nM4"
	payload := "{\"event\":\"created\",\"credentials\":{\"password\":\"" + secret + "\"}}\n"
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"),
		[]byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: true})
	if err == nil {
		t.Fatalf("fail-closed: JSON-секрет должен блокировать, report=%+v", report)
	}
	if report == nil || len(report.Violations) != 1 {
		t.Fatalf("ожидалась 1 violation, got %+v", report)
	}
	if report.Violations[0].Path != "runs/x/events.jsonl" {
		t.Fatalf("путь нарушения: %s", report.Violations[0].Path)
	}
}

// deepDoc — документ, поддерево которого глубже maxJSONDepth и потому не
// сканируется. Секрет внутри него сканеру не виден — именно поэтому факт
// пропуска обязан дойти до отчёта.
func deepDoc(secret string) string {
	doc := `{"password":"` + secret + `"}`
	for i := 0; i < maxJSONDepth+4; i++ {
		doc = `{"a":` + doc + `}`
	}
	return doc + "\n"
}

// QS-08: непросканированный участок — это не «clean». При fail-closed
// политике он блокирует так же, как находка: подтвердить чистоту нельзя.
func TestVerifyFailClosedOnUnscanned(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "deep.json"),
		[]byte(deepDoc("T0pSecretValue21k9XzW8qK2nM4")), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: true})
	if err == nil {
		t.Fatalf("fail-closed: непросканированный участок должен блокировать, report=%+v", report)
	}
	if report == nil || len(report.Unscanned) != 1 {
		t.Fatalf("ожидался 1 файл с непросканированным, got %+v", report)
	}
	if report.Unscanned[0].Path != "runs/x/deep.json" {
		t.Fatalf("путь непросканированного: %s", report.Unscanned[0].Path)
	}
	if len(report.Unscanned[0].Items) == 0 {
		t.Fatal("непросканированные участки без деталей")
	}
	if report.Verdict == "clean" || !strings.Contains(report.Verdict, "непросканированных") {
		t.Fatalf("вердикт не называет непросканированное: %q", report.Verdict)
	}
}

// QS-08: с выключенным блокером экспорт не останавливается, но слепая зона
// всё равно видна в отчёте — молча её терять нельзя ни при какой политике.
func TestVerifyReportsUnscannedWithBlockerDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "deep.json"),
		[]byte(deepDoc("T0pSecretValue21k9XzW8qK2nM4")), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: false})
	if err != nil {
		t.Fatalf("при выключенном блокере ошибки быть не должно: %v", err)
	}
	if len(report.Unscanned) != 1 {
		t.Fatalf("непросканированное потеряно: %+v", report)
	}
}

// QS-08: глубокое поддерево не должно обрывать обход файла — секрет на
// верхнем уровне того же документа обязан блокировать экспорт.
func TestVerifyBlocksSecretAfterDeepSubtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "x"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secret := "T0pSecretValue21k9XzW8qK2nM4"
	deep := strings.TrimSuffix(deepDoc("ignored-placeholder-value"), "\n")
	payload := `{"deep":` + deep + `,"client_secret":"` + secret + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "runs", "x", "events.jsonl"), []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(dir, Policy{FailOnSecrets: true})
	if err == nil {
		t.Fatalf("секрет после глубокого поддерева должен блокировать: %+v", report)
	}
	if len(report.Violations) != 1 || len(report.Violations[0].Findings) == 0 {
		t.Fatalf("находка не попала в отчёт: %+v", report)
	}
}
