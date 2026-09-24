package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

// QS-14 (#164). Мутация pkg/evidence/store.go:330-331: immutable-снимок run
// пишется с правами 0644 вместо 0444 — config.json и workflow.json, которыми
// заверяется resume, становятся перезаписываемыми на месте.
//
// Почему этого не ловил ни один существующий тест: 0444 нигде не
// утверждается. Наоборот, TestVerifyResumeEvidenceDetectsConfigTamper сам
// делает os.Chmod(configPath, 0644) перед записью — то есть обходит режим и
// потому одинаково работает с любым из них. Здесь проверяется ровно режим
// файла и ничего больше.
func TestRunSnapshotsArePublishedReadOnly(t *testing.T) {
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest("run-readonly"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "workflow.json"} {
		path := filepath.Join(store.RunDir(), name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatalf("%s отсутствует: %v", name, statErr)
		}
		// Проверяются именно write-биты, а не точное значение 0444: umask
		// процесса может снять группе/остальным read, но добавить write он не
		// может — то есть тест устойчив к окружению и при этом падает на
		// любом режиме, где write-бит появился.
		if perm := info.Mode().Perm(); perm&0222 != 0 {
			t.Fatalf("%s опубликован с правом записи (%04o): immutable-снимок обязан быть read-only", name, perm)
		} else if perm&0444 == 0 {
			t.Fatalf("%s нечитаем (%04o)", name, perm)
		}
	}
}

// QS-14 (#164). Мутация pkg/evidence/store.go:432-433: из условия выпадает
// checks.IsTestEvidence(check) — delivery авторизуется записью проверки,
// которая проверкой тестов не является (произвольная command-проверка).
//
// Почему этого не ловил ни один существующий тест: VerifyCheckEvidence вообще
// не имела прямого теста, а через pipeline в неё попадали только настоящие
// go-test записи, у которых IsTestEvidence истинен по построению. Предикат не
// мог отвергнуть вход ни в одном прогоне.
//
// Здесь публикуются два attempt в одном run: один с command-проверкой, другой
// с настоящей test-проверкой. Обе записи подлинные (VerifyResultDigest
// истинен), обе привязаны к своему workspace digest — все прочие конъюнкты
// строки 432 выполнены. Единственное, что разводит эти два случая, —
// IsTestEvidence.
func TestVerifyCheckEvidenceRejectsNonTestCheckRecord(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module example.test/evidence\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "check_test.go"),
		[]byte("package check\nimport \"testing\"\nfunc TestReal(t *testing.T) {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := checks.Runner{TargetDir: target}

	// Произвольная команда под классом unit: контроллер такую запись обязан
	// отвергать, хотя формально она «прошла».
	commandResult, err := runner.Run(context.Background(), checks.Definition{
		Name: "self-labelled", Class: "unit", Command: []string{"go", "version"}, Policy: checks.PolicyRequired,
	})
	if err != nil || commandResult.Status != checks.StatusPassed {
		t.Fatalf("command-проверка должна была пройти: %+v err=%v", commandResult, err)
	}
	if !checks.VerifyResultDigest(commandResult) || checks.IsTestEvidence(commandResult) {
		t.Fatalf("подготовка теста неверна: нужна подлинная, но не-тестовая запись: %+v", commandResult)
	}

	// Настоящее тестовое свидетельство — положительный контроль.
	testResult, err := runner.Run(context.Background(), checks.Definition{
		Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
		Command: []string{"go", "test", "-json", "-count=1", "./..."}, Policy: checks.PolicyRequired,
	})
	if err != nil || !checks.IsTestEvidence(testResult) {
		t.Fatalf("подготовка теста неверна: нужна настоящая test-запись: %+v err=%v", testResult, err)
	}

	// Каталог run держится вне target: содержимое workspace к моменту
	// публикации уже зафиксировано в записях проверок, и создавать под ним
	// новые файлы незачем.
	runsRoot := filepath.Join(t.TempDir(), "runs")
	store, err := Start(runsRoot, testRunManifest("run-checkevidence"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	publish := func(attemptID string, check checks.Result) {
		t.Helper()
		if err := store.PublishAttempt(AttemptManifest{
			AttemptID: attemptID, Stage: "check", StageIndex: 1,
			StartedAt: now, FinishedAt: now.Add(time.Second),
			Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
			Checks: []checks.Result{check},
		}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish("run-checkevidence-001-check", commandResult)
	publish("run-checkevidence-002-check", testResult)

	if err := VerifyCheckEvidence(runsRoot, "run-checkevidence",
		commandResult.EvidenceDigest, commandResult.WorkspaceDigestAfter); err == nil {
		t.Fatal("delivery авторизован не-тестовым evidence: command-проверка признана подтверждением")
	}
	// Положительный контроль: с настоящим тестовым evidence та же функция на
	// том же run обязана пройти, иначе тест падал бы по любой другой причине.
	if err := VerifyCheckEvidence(runsRoot, "run-checkevidence",
		testResult.EvidenceDigest, testResult.WorkspaceDigestAfter); err != nil {
		t.Fatalf("настоящее тестовое evidence должно авторизовать delivery: %v", err)
	}
}

// buildNonTerminalRunWithAttempt создаёт активный run с опубликованным attempt
// и событием attempt_finished, несущим manifest_sha256 — то есть доводит
// состояние до момента, в котором цикл проверки attempt-манифестов при resume
// вообще исполняется.
func buildNonTerminalRunWithAttempt(t *testing.T, runID string) (runDir, attemptID string) {
	t.Helper()
	target := t.TempDir()
	store, err := Start(filepath.Join(target, "runs"), testRunManifest(runID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	attemptID = runID + "-001-check"
	if err := store.Append(Event{Type: "run_started", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_started", Stage: "check", AttemptID: attemptID,
		Timestamp: now.Add(time.Second), Data: map[string]any{"stage_index": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishAttempt(AttemptManifest{
		AttemptID: attemptID, Stage: "check", StageIndex: 1,
		StartedAt: now.Add(time.Second), FinishedAt: now.Add(2 * time.Second),
		Status: "passed", Execution: "succeeded", Decision: "approved", Outcome: "passed",
	}, filepath.Join(target, "artifacts"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, _, manifestDigest, err := ArtifactDigest(filepath.Join(store.RunDir(), "attempts", attemptID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{Type: "attempt_finished", Stage: "check", AttemptID: attemptID,
		Timestamp: now.Add(2 * time.Second), Data: map[string]any{
			"status": "passed", "execution": "succeeded", "decision": "approved",
			"outcome": "passed", "manifest_sha256": manifestDigest,
		}}); err != nil {
		t.Fatal(err)
	}
	return store.RunDir(), attemptID
}

// sha256 пустого содержимого: дайджест файла нулевой длины вычисляется и
// сходится как у любого другого, поэтому «digest совпал» само по себе не
// означает, что манифест что-то содержит.
const emptyContentSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// rechainEventLog переписывает events.jsonl, применяя mutate к каждому
// событию, и полностью пересобирает hash-chain — как это сделал бы
// злоумышленник с доступом к каталогу run: цепочка после этого внутренне
// консистентна, и ReplayEventLog её принимает (у нетерминального run нет
// anchor, с которым можно было бы сверить корень).
func rechainEventLog(t *testing.T, runDir string, mutate func(*Event)) {
	t.Helper()
	path := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := genesisEventHash
	rebuilt := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		mutate(&event)
		event.PreviousSHA256 = previous
		digest, digestErr := eventDigest(event)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		event.SHA256 = digest
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		rebuilt = append(rebuilt, string(encoded))
		previous = digest
	}
	if err := os.WriteFile(path, []byte(strings.Join(rebuilt, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// QS-14 (#164). Мутация pkg/evidence/resumeverify.go:125-128: проверка
// attempt-манифеста при resume снимается — run продолжается поверх пустого
// manifest.json, то есть поверх attempt без единого свидетельства о том, что
// в нём происходило.
//
// Почему этого не ловил ни один существующий тест: buildNonTerminalRun
// создавал run без единого attempt_finished с manifest_sha256, поэтому цикл со
// строки 120 не делал ни одной итерации — тесты resume проверяли соседние
// guard'ы (config, workflow, цепочка событий) и до этого структурно не
// доходили.
//
// Важная деталь этого guard: три его конъюнкта из четырёх (digestErr,
// artifactType != "file", несовпадение дайджеста) дословно повторяют проверку
// в replay.go:126-128, а ReplayEventLog вызывается раньше — на строке 112.
// Единственное, чего там нет, — `size == 0`. Поэтому тест построен вокруг
// него: манифест обнуляется, а цепочка событий пересобирается так, чтобы
// manifest_sha256 указывал на дайджест пустого файла. ReplayEventLog такой
// вход принимает (что здесь и утверждается явно), и отвергнуть его может
// только `size == 0` в resumeverify.
func TestVerifyResumeEvidenceRejectsEmptyAttemptManifest(t *testing.T) {
	runDir, attemptID := buildNonTerminalRunWithAttempt(t, "run-resume-manifest")
	// Контроль: до подмены этот же run обязан проходить проверку, иначе тест
	// ловил бы не подмену манифеста, а дефект подготовки.
	if err := VerifyResumeEvidence(runDir); err != nil {
		t.Fatalf("нетронутый активный run с attempt должен проходить проверку: %v", err)
	}

	manifestPath := filepath.Join(runDir, "attempts", attemptID, "manifest.json")
	if err := os.Chmod(manifestPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	rechainEventLog(t, runDir, func(event *Event) {
		if event.Type == "attempt_finished" && event.Data != nil {
			event.Data["manifest_sha256"] = emptyContentSHA256
		}
	})

	// Первый guard подмену не видит: цепочка консистентна, тип артефакта —
	// file, дайджест совпадает с записанным в событии.
	if _, err := ReplayEventLog(filepath.Join(runDir, "events.jsonl"), "run-resume-manifest"); err != nil {
		t.Fatalf("подготовка теста неверна: пересобранная цепочка должна проходить replay: %v", err)
	}

	err := VerifyResumeEvidence(runDir)
	var resumeErr *ResumeEvidenceError
	if !errors.As(err, &resumeErr) || resumeErr.Reason != ReasonAttemptManifest {
		t.Fatalf("ожидали ReasonAttemptManifest на пустом manifest.json, получили %v", err)
	}
}

// QS-14 (#164). Мутация pkg/evidence/anchor.go:179-181: manifests_digest в
// anchor.json не сверяется с пересчитанным по цепочке — anchor перестаёт
// связывать терминальный run с набором attempt-манифестов, которые в нём
// зафиксированы.
//
// Почему этого не ловил ни один существующий тест: все три anchor-теста бьют
// по events.jsonl, а любое изменение журнала отвергается раньше — на
// event_count (строка 169) или chain_root_sha256 (строка 172). До сравнения
// manifests_digest они структурно не доходят.
//
// Здесь journal не тронут вовсе: event_count и chain_root_sha256 сходятся
// точно, run_id и terminal_event на месте. Отвергнуть подмену может только
// сравнение manifests_digest.
func TestVerifyAnchorDetectsManifestsDigestSubstitution(t *testing.T) {
	runDir := buildAnchoredRun(t, "run-anchor-manifests")
	if err := VerifyAnchor(runDir); err != nil {
		t.Fatalf("нетронутый anchor должен проходить проверку: %v", err)
	}
	anchor := readAnchor(t, runDir)
	if anchor.ManifestsDigest == "" {
		t.Fatal("подготовка теста неверна: anchor без manifests_digest")
	}
	original := anchor.ManifestsDigest
	anchor.ManifestsDigest = strings.Repeat("0", 64)
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(runDir, anchorFileName)
	if err := os.WriteFile(anchorPath, append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	err = VerifyAnchor(runDir)
	if err == nil {
		t.Fatal("подменённый manifests_digest должен ломать VerifyAnchor")
	}
	if !strings.Contains(err.Error(), "manifests_digest") {
		t.Fatalf("ожидалась ошибка manifests_digest, получено: %v", err)
	}

	// Возврат исходного значения обязан вернуть anchor в валидное состояние:
	// иначе тест ловил бы сам факт перезаписи anchor.json, а не расхождение.
	anchor.ManifestsDigest = original
	encoded, err = json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchorPath, append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAnchor(runDir); err != nil {
		t.Fatalf("восстановленный anchor должен проходить проверку: %v", err)
	}
}
