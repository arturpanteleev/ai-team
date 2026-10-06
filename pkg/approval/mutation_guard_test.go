package approval

import (
	"encoding/json"
	"strings"
	"testing"
)

// QS-14 (#164). Мутация pkg/approval/store.go:388-397: sameRequest возвращает
// true всегда (или теряет любое из своих сравнений) — повторный Create с тем
// же gate identity, но изменившимся subject-материалом принимается как «тот
// же самый уже ожидающий запрос» и возвращает старый approval.
//
// Почему этого не ловил ни один существующий тест: ID approval выводится из
// NewID(runID, attemptID, fromStage, toStage, trigger, subjectHash) — только
// из шести полей. Всё остальное (CandidateSHA256, Payload, роли, действия,
// targets, quorum, deferred) в identity не входит, и подменить его можно, не
// сдвинув ни ID, ни путь к файлу. Ни один тест не делал второй Create по тому
// же пути, поэтому ветка `if existing, loadErr := s.Load(...)` вообще не
// исполнялась, а sameRequest не имел тестового веса.
//
// Здесь первый Create создаёт запись, второй попадает ровно в тот же файл, и
// единственное, что может отвергнуть вход, — конкретное сравнение внутри
// sameRequest. Каждый подкейс меняет ровно одно поле.
func TestCreateRejectsChangedSubjectMaterialUnderSameApprovalID(t *testing.T) {
	// Payload задан скаляром намеренно: store пишет запись через
	// json.MarshalIndent, который переформатирует json.RawMessage, поэтому
	// объектный payload после round-trip никогда не сравнится байт в байт и
	// маскировал бы остальные сравнения sameRequest (отдельный латентный
	// дефект, к QS-14 не относится). Скалярная строка round-trip переживает.
	base := func() PendingApproval {
		return PendingApproval{
			RunID: "run-same", AttemptID: "attempt-1", FromStage: "deployer", ToStage: "deployer",
			Trigger: "delivery_plan", SubjectHash: testSubject,
			CandidateSHA256: strings.Repeat("b", 64),
			RequiredRoles:   []string{"release_manager"}, Quorum: QuorumAny,
			Actions: []string{"approve", "reject"},
			Targets: map[string]string{"approve": "deployer", "reject": "deployer"},
			Payload: json.RawMessage(`"original"`),
		}
	}
	// Контроль: точный повтор обязан быть идемпотентным. Без него любая из
	// проверок ниже проходила бы «по построению» — достаточно было бы, чтобы
	// второй Create отвергался всегда, а не из-за изменившегося поля.
	t.Run("точный повтор идемпотентен", func(t *testing.T) {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		first, err := store.Create(base())
		if err != nil {
			t.Fatal(err)
		}
		again, err := store.Create(base())
		if err != nil || again.ID != first.ID {
			t.Fatalf("точный повтор должен возвращать тот же approval: %+v err=%v", again, err)
		}
	})
	cases := map[string]func(*PendingApproval){
		// Главный кейс из issue: тот же gate, другой candidate — то есть
		// человеку показали один артефакт, а доставить предлагают другой.
		"другой candidate": func(v *PendingApproval) { v.CandidateSHA256 = strings.Repeat("c", 64) },
		"candidate убран":  func(v *PendingApproval) { v.CandidateSHA256 = "" },
		// Payload — машиночитаемый subject (canonical JSON delivery plan).
		"другой payload": func(v *PendingApproval) { v.Payload = json.RawMessage(`"swapped"`) },
		// Ниже — поля, определяющие, кто и что именно может решить.
		"другие роли":    func(v *PendingApproval) { v.RequiredRoles = []string{"developer"} },
		"другой quorum":  func(v *PendingApproval) { v.Quorum = QuorumAll },
		"другие actions": func(v *PendingApproval) { v.Actions = []string{"approve"} },
		"другие targets": func(v *PendingApproval) { v.Targets = map[string]string{"approve": "coder", "reject": "coder"} },
		"другой deferred": func(v *PendingApproval) {
			v.Deferred = true
			v.RequiredRoles = []string{"release_manager"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			first, err := store.Create(base())
			if err != nil {
				t.Fatal(err)
			}
			second := base()
			mutate(&second)
			created, err := store.Create(second)
			if err == nil {
				t.Fatalf("изменившийся subject-материал принят как «уже ожидающий»: %+v", created)
			}
			if !strings.Contains(err.Error(), "другим subject") {
				t.Fatalf("отклонить должен именно sameRequest, а не сосед: %v", err)
			}
			// Контроль идентичности: ID действительно совпал, то есть второй
			// Create бил в ту же запись, а не создавал соседнюю.
			if second.ID != "" && second.ID != first.ID {
				t.Fatalf("подготовка теста неверна: ID разошлись (%s vs %s)", second.ID, first.ID)
			}
			reloaded, loadErr := store.Load(first.RunID, first.ID)
			if loadErr != nil || reloaded.CandidateSHA256 != first.CandidateSHA256 ||
				!strings.Contains(string(reloaded.Payload), "original") {
				t.Fatalf("исходный approval не должен быть перезаписан: %+v err=%v", reloaded, loadErr)
			}
		})
	}
}

// QS-14 (#164). Продолжение того же guard: при явно заданном ID подменить
// можно и SubjectHash — путь к файлу определяется парой (runID, ID), а не
// хешом subject. Сравнение left.SubjectHash == right.SubjectHash в sameRequest
// здесь единственное, что отделяет подмену предмета решения от идемпотентного
// повтора.
func TestCreateRejectsChangedSubjectHashUnderExplicitID(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := PendingApproval{
		ID: "approval-fixed-id", RunID: "run-explicit", AttemptID: "attempt-1",
		FromStage: "coder", ToStage: "reviewer", Trigger: "stage_completed",
		SubjectHash:   testSubject,
		RequiredRoles: []string{"developer"}, Actions: []string{"approve"},
		Targets: map[string]string{"approve": "reviewer"},
	}
	if _, err := store.Create(base); err != nil {
		t.Fatal(err)
	}
	swapped := base
	swapped.SubjectHash = strings.Repeat("f", 64)
	if _, err := store.Create(swapped); err == nil || !strings.Contains(err.Error(), "другим subject") {
		t.Fatalf("подменённый subject_hash под тем же ID должен быть отклонён, got %v", err)
	}
	// Точный повтор обязан остаться идемпотентным, иначе мутация «sameRequest
	// всегда false» прошла бы незамеченной.
	repeated, err := store.Create(base)
	if err != nil || repeated.ID != base.ID {
		t.Fatalf("точный повтор должен быть идемпотентным: %+v err=%v", repeated, err)
	}
}
