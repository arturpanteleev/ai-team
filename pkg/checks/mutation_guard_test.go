package checks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// QS-14 (#164). Мутация pkg/checks/checks.go:652-653: режим файла удаляется из
// per-file хеша workspace digest — `chmod +x`, сделанный между проверками и
// доставкой, перестаёт менять fingerprint и становится невидим для контроллера.
//
// Почему этого не ловил ни один существующий тест: все они сравнивали digest
// после изменения *содержимого* файла (или состава дерева), а содержимое и так
// попадает в хеш через io.Copy — отклонение обеспечивал соседний вклад, до
// вклада режима тест структурно не доходил. Здесь содержимое, имя, состав
// дерева и время неизменны побайтово, и единственное, что вообще может
// развести два fingerprint, — режим файла.
func TestWorkspaceDigestBindsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows бит исполнения не моделируется файловым режимом")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "deploy.sh")
	const body = "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Единственное изменение — бит исполнения. Содержимое не трогаем вовсе.
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	after, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Fatalf("подготовка теста неверна: содержимое изменилось (%q)", data)
	}
	if before == after {
		t.Fatalf("chmod +x обязан менять workspace digest, иначе он невидим между проверкой и доставкой: %s", before)
	}

	// Возврат режима обязан вернуть исходный digest: иначе тест ловил бы не
	// режим, а любой побочный эффект повторного обхода дерева.
	if err := os.Chmod(script, 0644); err != nil {
		t.Fatal(err)
	}
	restored, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if restored != before {
		t.Fatalf("digest обязан быть функцией только состояния дерева: %s != %s", restored, before)
	}

	// Тот же инвариант на уровне per-file хеша: меняется запись именно того
	// файла, которому сменили режим.
	perFileBefore, _, err := WorkspaceFileDigests(dir, DefaultIgnoreDirs())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	perFileAfter, _, err := WorkspaceFileDigests(dir, DefaultIgnoreDirs())
	if err != nil {
		t.Fatal(err)
	}
	if perFileBefore["deploy.sh"] == perFileAfter["deploy.sh"] {
		t.Fatalf("per-file digest обязан включать режим: %s", perFileAfter["deploy.sh"])
	}
}

// QS-14 (#164). Мутация pkg/checks/checks.go:580-581: VerifyResultDigest
// сводится к «EvidenceDigest не пуст» — запись о проверке можно
// отредактировать (статус, число тестов, workspace-привязку), не трогая
// evidence_digest, и она по-прежнему считается подлинной.
//
// Почему этого не ловил ни один существующий тест: единственное обращение
// (junit_adapter_test.go:63) утверждало VerifyResultDigest(result) == true на
// свежепосчитанной записи, где digest и содержимое совпадают по построению.
// Предикат равенства там физически не мог отвергнуть вход. Здесь digest всегда
// непуст и синтаксически корректен, и отклонить запись может только сравнение
// с пересчитанным дайджестом.
func TestVerifyResultDigestRejectsEditedRecordWithIntactDigest(t *testing.T) {
	authentic := Result{
		Name: "unit", Class: "unit", Adapter: AdapterGoTest, Command: []string{"go", "test", "./..."},
		Policy: PolicyRequired, WorkingDir: ".", Status: StatusFailed, ExitCode: 1,
		StartedAt: time.Unix(1700000000, 0).UTC(), FinishedAt: time.Unix(1700000010, 0).UTC(),
		WorkspaceDigestBefore: strings.Repeat("a", 64), WorkspaceDigestAfter: strings.Repeat("a", 64),
		DiscoveredTests: 3, PassedTests: 1, FailedTests: 2,
	}
	authentic.EvidenceDigest = resultDigest(authentic)
	if !VerifyResultDigest(authentic) {
		t.Fatal("подлинная запись должна проходить проверку дайджеста")
	}

	// Каждая правка — одно поле, evidence_digest остаётся прежним (непустым и
	// hex-корректным), поэтому «не пусто» её не отличает.
	edits := map[string]func(*Result){
		"провал переписан в успех": func(r *Result) {
			r.Status = StatusPassed
			r.ExitCode = 0
			r.FailedTests = 0
			r.PassedTests = 3
		},
		"подменён workspace after": func(r *Result) { r.WorkspaceDigestAfter = strings.Repeat("b", 64) },
		"дорисованы тесты":         func(r *Result) { r.DiscoveredTests = 99 },
		"подменена команда":        func(r *Result) { r.Command = []string{"true"} },
		"policy понижен":           func(r *Result) { r.Policy = PolicyOptional },
		"подменён adapter":         func(r *Result) { r.Adapter = AdapterCommand },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			edited := authentic
			edit(&edited)
			if edited.EvidenceDigest != authentic.EvidenceDigest {
				t.Fatal("подготовка теста неверна: правка задела сам evidence_digest")
			}
			if VerifyResultDigest(edited) {
				t.Fatal("отредактированная запись с нетронутым evidence_digest признана подлинной")
			}
		})
	}

	// Чужой, но синтаксически безупречный дайджест — тот же класс подделки.
	foreign := authentic
	foreign.EvidenceDigest = strings.Repeat("0", 64)
	if VerifyResultDigest(foreign) {
		t.Fatal("чужой evidence_digest не должен считаться подлинным")
	}
}

// QS-14 (#164). Мутация pkg/evidence/store.go:433: из условия выпадает
// checks.IsTestEvidence(check) — delivery проходит на записи проверки, которая
// не является тестовым свидетельством (произвольная command-проверка, optional
// policy, ноль выполненных тестов).
//
// Здесь фиксируется контракт самого предиката, а сквозной кейс на
// VerifyCheckEvidence — в pkg/evidence. Каждый подкейс ломает ровно одно
// условие IsTestEvidence при прочих валидных.
func TestIsTestEvidenceRejectsNonTestCheck(t *testing.T) {
	base := Result{
		Name: "unit", Class: "unit", Adapter: AdapterGoTest, Policy: PolicyRequired,
		Status: StatusPassed, DiscoveredTests: 3, PassedTests: 3,
	}
	if !IsTestEvidence(base) {
		t.Fatal("подготовка теста неверна: базовая запись должна быть тестовым evidence")
	}
	cases := map[string]func(*Result){
		"проверка не прошла":      func(r *Result) { r.Status = StatusFailed },
		"policy не required":      func(r *Result) { r.Policy = PolicyOptional },
		"класс не тестовый":       func(r *Result) { r.Class = "lint" },
		"произвольная команда":    func(r *Result) { r.Adapter = AdapterCommand },
		"ни одного теста найдено": func(r *Result) { r.DiscoveredTests = 0 },
		"ни одного теста прошло":  func(r *Result) { r.PassedTests = 0 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			value := base
			edit(&value)
			if IsTestEvidence(value) {
				t.Fatal("не-тестовая запись признана тестовым evidence")
			}
		})
	}
}
