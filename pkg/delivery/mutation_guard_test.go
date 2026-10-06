package delivery

import (
	"strings"
	"testing"
)

// QS-14 (#164). Мутация pkg/delivery/plan.go:145-149: тело цикла валидации
// precondition evidence удаляется — план со структурно пустым или поддельным
// evidence предусловий проходит Validate и попадает под approval.
//
// Почему этого не ловил ни один существующий тест: все они строят план через
// validTestPlan()/BuildPlan, где preconditions валидны по построению, а
// отклонение обеспечивает более ранний guard `len(p.Preconditions) == 0`
// (plan.go:142). До подпредикатов внутри цикла тест структурно не доходит.
// Здесь карта непуста всегда, все прочие поля плана валидны, и единственное,
// что может отвергнуть вход, — конкретный подпредикат внутри цикла. Каждый
// подкейс ломает ровно одно поле evidence.
func TestPlanRejectsStructurallyInvalidPreconditionEvidence(t *testing.T) {
	// Контроль: базовый план обязан быть валиден, иначе тест ловил бы не то.
	if err := validTestPlan().Validate(); err != nil {
		t.Fatalf("базовый план должен быть валиден: %v", err)
	}
	cases := map[string]PreconditionEvidence{
		// Пустое evidence: план несёт имя предусловия без единого атрибута —
		// ровно тот случай «пустой карты», который мутация делает одобряемым.
		"пустое evidence":      {},
		"тип не file":          {Type: "blob", Size: 10, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
		"нулевой размер":       {Type: "file", Size: 0, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
		"отрицательный размер": {Type: "file", Size: -1, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
		"sha256 не hex":        {Type: "file", Size: 10, SHA256: strings.Repeat("z", 64), Verdict: "APPROVED"},
		"sha256 усечён":        {Type: "file", Size: 10, SHA256: strings.Repeat("e", 63), Verdict: "APPROVED"},
		"пустой sha256":        {Type: "file", Size: 10, SHA256: "", Verdict: "APPROVED"},
		"пустой verdict":       {Type: "file", Size: 10, SHA256: strings.Repeat("e", 64), Verdict: ""},
	}
	for name, evidence := range cases {
		t.Run(name, func(t *testing.T) {
			plan := validTestPlan()
			plan.Preconditions = map[string]PreconditionEvidence{"review": evidence}
			err := plan.Validate()
			if err == nil {
				t.Fatal("невалидное precondition evidence должно отклонять план")
			}
			if !strings.Contains(err.Error(), "precondition") {
				t.Fatalf("отклонить должен именно guard precondition evidence, а не сосед: %v", err)
			}
			// Такой план не должен получать канонический hash: иначе его
			// можно было бы одобрить через --approve-plan <sha256>.
			if _, hashErr := plan.Hash(); hashErr == nil {
				t.Fatal("невалидный план не должен иметь canonical hash")
			}
		})
	}
	// Имя предусловия — часть того же guard: пустое имя или имя с краевыми
	// пробелами делает evidence неадресуемым — сопоставить его с файлом на
	// диске невозможно.
	for _, badName := range []string{"", " review", "review "} {
		plan := validTestPlan()
		plan.Preconditions = map[string]PreconditionEvidence{
			badName: {Type: "file", Size: 10, SHA256: strings.Repeat("e", 64), Verdict: "APPROVED"},
		}
		if err := plan.Validate(); err == nil {
			t.Fatalf("имя предусловия %q должно отклоняться", badName)
		}
	}
}
