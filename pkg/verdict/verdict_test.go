package verdict

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    Verdict
	}{
		{"approved", "# Review\n\n**Verdict:** APPROVED\n", Approved},
		{"changes_requested", "**Verdict:** CHANGES_REQUESTED", ChangesRequested},
		{"rejected", "text\n**Verdict:** REJECTED\nmore", Rejected},
		{"pass", "# Report\n**Result:** PASS\n", Pass},
		{"fail", "**Result:** FAIL", Fail},
		{"trailing_spaces", "**Verdict:** APPROVED   \n", Approved},
		{"empty", "", None},
		{"no_verdict", "просто текст без вердикта", None},
		{"prose_mention_not_matched", "рекомендую формат **Verdict:** APPROVED в конце файла", None},
		{"quoted_in_list_not_matched", "- пример: **Verdict:** REJECTED — так писать", None},
		{"first_wins", "**Verdict:** REJECTED\n\n**Verdict:** APPROVED", Rejected},
		{"first_wins_mixed", "**Result:** FAIL\n**Verdict:** APPROVED", Fail},
		{"lowercase_not_matched", "**verdict:** approved", None},
		{"unknown_value_not_matched", "**Verdict:** MAYBE", None},
		{"fenced_verdict_is_data", "```\n**Verdict:** REJECTED\n```\n", None},
		{"tilde_fenced_is_data", "~~~\n**Result:** FAIL\n~~~\n", None},
		{"fenced_with_infostring_is_data", "```md\n**Verdict:** REJECTED\n```\n", None},
		{"unclosed_fence_hides_to_end", "```\n**Verdict:** REJECTED\n", None},
		{"blockquote_is_data", "> **Verdict:** REJECTED\n", None},
		{"blockquote_spaced_is_data", "> **Result:** FAIL\n", None},
		{"marker_before_fence_parsed", "**Verdict:** APPROVED\n```\n**Verdict:** REJECTED\n```\n", Approved},
		{"marker_after_fence_parsed", "```\n**Verdict:** REJECTED\n```\n**Verdict:** APPROVED\n", Approved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.content); got != tt.want {
				t.Errorf("Parse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNegative(t *testing.T) {
	for _, v := range []Verdict{Rejected, ChangesRequested, Fail} {
		if !v.IsNegative() {
			t.Errorf("%s должен быть негативным", v)
		}
	}
	for _, v := range []Verdict{Approved, Pass, None} {
		if v.IsNegative() {
			t.Errorf("%s не должен быть негативным", v)
		}
	}
}

func TestFromOutputs(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "review.md")
	second := filepath.Join(dir, "extra.md")
	other := filepath.Join(dir, "data.json")
	os.WriteFile(first, []byte("нет вердикта"), 0644)
	os.WriteFile(second, []byte("**Verdict:** APPROVED\n"), 0644)
	os.WriteFile(other, []byte("**Verdict:** REJECTED\n"), 0644)

	if got := FromOutputs([]string{first, second, other}); got != Approved {
		t.Errorf("FromOutputs() = %q, want APPROVED (json игнорируется, пустой md пропускается)", got)
	}
	if got := FromOutputs([]string{first}); got != None {
		t.Errorf("FromOutputs() = %q, want None", got)
	}
	if got := FromOutputs(nil); got != None {
		t.Errorf("FromOutputs(nil) = %q, want None", got)
	}
}

func TestFromOutputsContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	contract := &Contract{Required: true, Marker: "Verdict", Values: []Verdict{Approved, Rejected}}

	os.WriteFile(path, []byte("**Verdict:** APPROVED\n"), 0644)
	got, err := FromOutputsContract([]string{path}, contract)
	if err != nil || got != Approved {
		t.Fatalf("valid contract: got=%q err=%v", got, err)
	}

	os.WriteFile(path, []byte("нет маркера\n"), 0644)
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("missing marker должен быть ошибкой")
	}

	os.WriteFile(path, []byte("**Verdict:** REJECTED\n**Verdict:** APPROVED\n"), 0644)
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("multiple markers должны быть ошибкой")
	}
	os.WriteFile(path, []byte("**Verdict:** APPROVED\n**Result:** FAIL\n"), 0644)
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("mixed control markers must be rejected")
	}

	os.WriteFile(path, []byte("**Verdict:** MAYBE\n"), 0644)
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("unknown marker value должен быть ошибкой")
	}
}

func TestFencedMarkerIgnoredByContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	contract := &Contract{Required: true, Marker: "Verdict", Values: []Verdict{Approved, Rejected}}

	os.WriteFile(path, []byte("```\n**Verdict:** REJECTED\n```\n"), 0644)
	_, err := FromOutputsContract([]string{path}, contract)
	if err == nil {
		t.Fatal("fenced маркер не должен удовлетворять contract (маркер — данные, не сигнал)")
	}
	if !strings.Contains(err.Error(), "отсутствует") {
		t.Errorf("ожидалась ошибка про отсутствие маркера, got: %v", err)
	}
}

func TestMultipleMarkersCountedFromControlRegionsOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	contract := &Contract{Required: true, Marker: "Verdict", Values: []Verdict{Approved, Rejected}}

	os.WriteFile(path, []byte("```\n**Verdict:** APPROVED\n```\n**Verdict:** REJECTED\n"), 0644)
	got, err := FromOutputsContract([]string{path}, contract)
	if err != nil || got != Rejected {
		t.Fatalf("fenced маркер не считается; got=%q err=%v", got, err)
	}
}

func TestReadBlockedIgnoresDataRegions(t *testing.T) {
	root := t.TempDir()
	feature := "my-feature"
	statusDir := filepath.Join(root, feature, "status")
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(statusDir, "quoted.md"),
		[]byte("> **Status:** BLOCKED\n> **Blocker:** цитата-пример\n"), 0644)
	if blocked, _ := ReadBlocked(root, feature, "quoted"); blocked {
		t.Fatal("BLOCKED в blockquote — данные, не сигнал")
	}

	os.WriteFile(filepath.Join(statusDir, "fenced.md"),
		[]byte("```\n**Status:** BLOCKED\n**Blocker:** фейковый\n```\n"), 0644)
	if blocked, _ := ReadBlocked(root, feature, "fenced"); blocked {
		t.Fatal("BLOCKED в fenced code block — данные, не сигнал")
	}

	os.WriteFile(filepath.Join(statusDir, "plain.md"),
		[]byte("**Status:** BLOCKED\n**Blocker:** настоящий\n"), 0644)
	blocked, reason := ReadBlocked(root, feature, "plain")
	if !blocked || reason != "настоящий" {
		t.Fatalf("обычный маркер должен читаться: blocked=%v reason=%q", blocked, reason)
	}
}

func TestReadBlocked(t *testing.T) {
	root := t.TempDir()
	feature := "my-feature"

	if blocked, _ := ReadBlocked(root, feature, "analyst"); blocked {
		t.Fatal("нет status-файла — не должен быть blocked")
	}

	statusDir := filepath.Join(root, feature, "status")
	os.MkdirAll(statusDir, 0755)
	os.WriteFile(filepath.Join(statusDir, "analyst.md"),
		[]byte("**Status:** BLOCKED\n**Blocker:** требования противоречивы\n"), 0644)

	blocked, reason := ReadBlocked(root, feature, "analyst")
	if !blocked {
		t.Fatal("ожидался blocked")
	}
	if reason != "требования противоречивы" {
		t.Errorf("reason = %q", reason)
	}

	os.WriteFile(filepath.Join(statusDir, "architect.md"), []byte("**Status:** BLOCKED\n"), 0644)
	blocked, reason = ReadBlocked(root, feature, "architect")
	if !blocked || reason != "причина не указана" {
		t.Errorf("blocked=%v reason=%q", blocked, reason)
	}

	os.WriteFile(filepath.Join(statusDir, "coder.md"), []byte("всё в порядке"), 0644)
	if blocked, _ := ReadBlocked(root, feature, "coder"); blocked {
		t.Error("файл без маркера BLOCKED не должен блокировать")
	}
}

// Contract-тест: формат, который инструкции требуют от агента, распознаётся парсером.
func TestContract_InstructionMatchesParser(t *testing.T) {
	// Пример артефакта, буквально следующего VerdictInstruction.
	artifact := "# Ревью\n\nЗамечаний нет.\n\n**Verdict:** APPROVED\n"
	if Parse(artifact) != Approved {
		t.Error("артефакт по инструкции VerdictInstruction не распознан")
	}

	instr := VerdictInstruction("Verdict", Approved, ChangesRequested, Rejected)
	for _, v := range []string{"APPROVED", "CHANGES_REQUESTED", "REJECTED"} {
		if !contains(instr, v) {
			t.Errorf("инструкция не перечисляет значение %s", v)
		}
	}
	// Формат из инструкции — «**Verdict:** X» — собираем и парсим каждое значение.
	for _, v := range []Verdict{Approved, ChangesRequested, Rejected} {
		line := "**Verdict:** " + string(v) + "\n"
		if Parse(line) != v {
			t.Errorf("канонический формат %q не распознан", line)
		}
	}

	// BLOCKED-инструкция и ReadBlocked согласованы.
	root := t.TempDir()
	instrBlocked := BlockedInstruction(root, "f", "analyst")
	if !contains(instrBlocked, StatusFilePath(root, "f", "analyst")) {
		t.Error("BlockedInstruction не содержит путь status-файла")
	}
	os.MkdirAll(filepath.Join(root, "f", "status"), 0755)
	os.WriteFile(StatusFilePath(root, "f", "analyst"),
		[]byte("**Status:** BLOCKED\n**Blocker:** тест\n"), 0644)
	if blocked, reason := ReadBlocked(root, "f", "analyst"); !blocked || reason != "тест" {
		t.Error("формат из BlockedInstruction не распознан ReadBlocked")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
