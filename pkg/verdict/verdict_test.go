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

		// QS-22: значение обязано стоять на одной строке с маркером.
		{"newline_between_marker_and_value_not_matched", "**Verdict:**\nAPPROVED\n", None},
		{"blank_lines_between_marker_and_value_not_matched", "**Verdict:**\n\n\nREJECTED\n", None},
		{"result_newline_between_marker_and_value_not_matched", "**Result:**\nPASS\n", None},
		{"marker_without_value_not_matched", "**Verdict:**\n", None},
		// Мост через fenced-блок: содержимое региона маскируется пробелами,
		// и раньше маркер склеивался со значением, стоящим ПОСЛЕ блока.
		{"fenced_region_is_not_a_bridge", "**Verdict:**\n```\n**Verdict:** CHANGES_REQUESTED\n```\nAPPROVED\n", None},
		{"blockquote_region_is_not_a_bridge", "**Verdict:**\n> **Verdict:** CHANGES_REQUESTED\nAPPROVED\n", None},

		// Легитимные однострочные формы продолжают распознаваться.
		{"tab_separator_matched", "**Verdict:**\tAPPROVED\n", Approved},
		{"multiple_spaces_matched", "**Verdict:**   APPROVED\n", Approved},
		{"trailing_tabs_matched", "**Result:** PASS\t\t\n", Pass},
		{"crlf_line_ending_matched", "# Review\r\n\r\n**Verdict:** APPROVED\r\n", Approved},
		{"crlf_result_matched", "**Result:** FAIL\r\n", Fail},
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
	if err := os.WriteFile(first, []byte("нет вердикта"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(second, []byte("**Verdict:** APPROVED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(other, []byte("**Verdict:** REJECTED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

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

	if err := os.WriteFile(path, []byte("**Verdict:** APPROVED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := FromOutputsContract([]string{path}, contract)
	if err != nil || got != Approved {
		t.Fatalf("valid contract: got=%q err=%v", got, err)
	}

	if err := os.WriteFile(path, []byte("нет маркера\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("missing marker должен быть ошибкой")
	}

	if err := os.WriteFile(path, []byte("**Verdict:** REJECTED\n**Verdict:** APPROVED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("multiple markers должны быть ошибкой")
	}
	if err := os.WriteFile(path, []byte("**Verdict:** APPROVED\n**Result:** FAIL\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("mixed control markers must be rejected")
	}

	if err := os.WriteFile(path, []byte("**Verdict:** MAYBE\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := FromOutputsContract([]string{path}, contract); err == nil {
		t.Fatal("unknown marker value должен быть ошибкой")
	}
}

func TestFencedMarkerIgnoredByContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	contract := &Contract{Required: true, Marker: "Verdict", Values: []Verdict{Approved, Rejected}}

	if err := os.WriteFile(path, []byte("```\n**Verdict:** REJECTED\n```\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
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

	if err := os.WriteFile(path, []byte("```\n**Verdict:** APPROVED\n```\n**Verdict:** REJECTED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := FromOutputsContract([]string{path}, contract)
	if err != nil || got != Rejected {
		t.Fatalf("fenced маркер не считается; got=%q err=%v", got, err)
	}
}

// QS-22: строгий contract-путь тоже требует значение на строке маркера —
// иначе перевод строки (в том числе через маскированный fenced-блок) выдавал
// бы вердикт, которого агент не писал.
func TestContractRequiresValueOnMarkerLine(t *testing.T) {
	dir := t.TempDir()
	contract := &Contract{Required: true, Marker: "Verdict", Values: []Verdict{Approved, ChangesRequested, Rejected}}

	cases := []struct{ name, content string }{
		{"newline_between_marker_and_value", "**Verdict:**\nAPPROVED\n"},
		{"fenced_region_bridge", "**Verdict:**\n```\n**Verdict:** CHANGES_REQUESTED\n```\nAPPROVED\n"},
		{"blockquote_bridge", "**Verdict:**\n> **Verdict:** CHANGES_REQUESTED\nAPPROVED\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".md")
			writeFile(t, path, tt.content)
			got, err := FromOutputsContract([]string{path}, contract)
			if err == nil {
				t.Fatalf("значение не на строке маркера не должно давать вердикт; got=%q", got)
			}
			if !strings.Contains(err.Error(), "отсутствует") {
				t.Errorf("ожидалась ошибка про отсутствие маркера, got: %v", err)
			}
		})
	}

	// Канонические однострочные формы contract принимает как раньше.
	for name, content := range map[string]string{
		"tab_separator": "**Verdict:**\tAPPROVED\n",
		"crlf":          "**Verdict:** APPROVED\r\n",
	} {
		path := filepath.Join(dir, name+"-ok.md")
		writeFile(t, path, content)
		got, err := FromOutputsContract([]string{path}, contract)
		if err != nil || got != Approved {
			t.Errorf("%s: got=%q err=%v, want APPROVED", name, got, err)
		}
	}
}

// QS-22: BLOCKED-протокол читает **Status:** и **Blocker:** по тем же
// правилам — значение только на строке маркера.
func TestReadBlockedRequiresValueOnMarkerLine(t *testing.T) {
	root := t.TempDir()
	feature := "f"
	statusDir := filepath.Join(root, feature, "status")
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(statusDir, "split.md"), "**Status:**\nBLOCKED\n")
	if blocked, _ := ReadBlocked(root, feature, "split"); blocked {
		t.Error("BLOCKED на следующей строке — не сигнал")
	}

	writeFile(t, filepath.Join(statusDir, "bridged.md"),
		"**Status:**\n```\n**Status:** BLOCKED\n```\nBLOCKED\n")
	if blocked, _ := ReadBlocked(root, feature, "bridged"); blocked {
		t.Error("fenced-регион не должен работать мостом к BLOCKED")
	}

	// Blocker без причины на своей строке: блокировка настоящая, но причину
	// нельзя подтянуть со следующей строки.
	writeFile(t, filepath.Join(statusDir, "reason.md"),
		"**Status:** BLOCKED\n**Blocker:**\nпридуманная причина\n")
	blocked, reason := ReadBlocked(root, feature, "reason")
	if !blocked {
		t.Fatal("ожидался blocked")
	}
	if reason != "причина не указана" {
		t.Errorf("reason = %q, want %q", reason, "причина не указана")
	}

	// Канонические однострочные формы, включая CRLF.
	writeFile(t, filepath.Join(statusDir, "crlf.md"),
		"**Status:** BLOCKED\r\n**Blocker:** требования противоречивы\r\n")
	blocked, reason = ReadBlocked(root, feature, "crlf")
	if !blocked || reason != "требования противоречивы" {
		t.Errorf("CRLF: blocked=%v reason=%q", blocked, reason)
	}
}

func TestReadBlockedIgnoresDataRegions(t *testing.T) {
	root := t.TempDir()
	feature := "my-feature"
	statusDir := filepath.Join(root, feature, "status")
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statusDir, "quoted.md"),
		[]byte("> **Status:** BLOCKED\n> **Blocker:** цитата-пример\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if blocked, _ := ReadBlocked(root, feature, "quoted"); blocked {
		t.Fatal("BLOCKED в blockquote — данные, не сигнал")
	}

	if err := os.WriteFile(filepath.Join(statusDir, "fenced.md"),
		[]byte("```\n**Status:** BLOCKED\n**Blocker:** фейковый\n```\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if blocked, _ := ReadBlocked(root, feature, "fenced"); blocked {
		t.Fatal("BLOCKED в fenced code block — данные, не сигнал")
	}

	if err := os.WriteFile(filepath.Join(statusDir, "plain.md"),
		[]byte("**Status:** BLOCKED\n**Blocker:** настоящий\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
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
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(statusDir, "analyst.md"),
		[]byte("**Status:** BLOCKED\n**Blocker:** требования противоречивы\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	blocked, reason := ReadBlocked(root, feature, "analyst")
	if !blocked {
		t.Fatal("ожидался blocked")
	}
	if reason != "требования противоречивы" {
		t.Errorf("reason = %q", reason)
	}

	if err := os.WriteFile(filepath.Join(statusDir, "architect.md"), []byte("**Status:** BLOCKED\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	blocked, reason = ReadBlocked(root, feature, "architect")
	if !blocked || reason != "причина не указана" {
		t.Errorf("blocked=%v reason=%q", blocked, reason)
	}

	if err := os.WriteFile(filepath.Join(statusDir, "coder.md"), []byte("всё в порядке"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
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
	if err := os.MkdirAll(filepath.Join(root, "f", "status"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(StatusFilePath(root, "f", "analyst"),
		[]byte("**Status:** BLOCKED\n**Blocker:** тест\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if blocked, reason := ReadBlocked(root, "f", "analyst"); !blocked || reason != "тест" {
		t.Error("формат из BlockedInstruction не распознан ReadBlocked")
	}
}

// writeFile — запись фикстуры с проверкой ошибки: неудавшаяся подготовка
// теста должна падать явно, а не превращаться в непонятный отрицательный
// результат (и errcheck на это смотрит).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("подготовка фикстуры %s: %v", path, err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
