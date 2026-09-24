package redact

import (
	"strings"
	"testing"
)

// QS-14 (#164). Мутация pkg/redact/scanner.go:182-184: порог длины
// `len(value) < 16` снимается — сканер начинает считать секретом любое
// короткое буквенно-цифровое значение секретного поля, и fail-closed блок
// экспорта срабатывает на мусоре.
//
// Почему этого не ловил ни один существующий тест: line-based правило
// «secret assignment» само требует от значения {16,} символов, поэтому через
// Scan текста до строки 183 короткое значение просто не доходит. Единственный
// путь, где порог действительно работает, — структурный JSON-разбор
// (scanner.go:249/268), у которого своего порога нет. Все негативные фикстуры
// TestScanJSONIgnoresPlaceholders («your-password», «changeme»,
// «placeholder») короче 16 символов и отсекались именно этим порогом, а не
// фильтром плейсхолдеров, как гласит имя теста.
//
// Здесь значения короткие, но во всём остальном «секретоподобные»: есть и
// буква, и цифра, нет ни одного маркера-плейсхолдера, нет ни <>, ни ${, ни
// ведущего $. Отвергнуть их может только порог длины.
func TestScanJSONLengthThresholdIsSoleRejectorForShortValues(t *testing.T) {
	for _, value := range []string{"ab1", "k3y", "tok3n42", "a1b2c3d4e5f6g7h"} {
		if len(value) >= 16 {
			t.Fatalf("подготовка теста неверна: %q не короче порога", value)
		}
		if !hasLetterAndDigit(value) {
			t.Fatalf("подготовка теста неверна: %q не проходит проверку буква+цифра", value)
		}
		if containsPlaceholderMarker(value) {
			t.Fatalf("подготовка теста неверна: %q отсекается фильтром плейсхолдеров", value)
		}
		if likelySecretValue(value) {
			t.Fatalf("короткое значение %q признано секретом", value)
		}
		input := []byte(`{"api_key": "` + value + `", "token": "` + value + `"}`)
		if findings := Scan(input); len(findings) != 0 {
			t.Fatalf("короткое значение %q дало находки: %+v", value, findings)
		}
	}
	// Положительный контроль: то же самое значение, дотянутое до порога,
	// секретом считается. Без него тест проходил бы и при «likelySecretValue
	// всегда false».
	long := "a1b2c3d4e5f6g7h8"
	if len(long) < 16 {
		t.Fatal("подготовка теста неверна: контрольное значение короче порога")
	}
	if !likelySecretValue(long) {
		t.Fatalf("значение %q длиной %d должно считаться секретом", long, len(long))
	}
	if findings := Scan([]byte(`{"api_key": "` + long + `"}`)); len(findings) == 0 {
		t.Fatalf("значение длиной %d в секретном поле должно давать находку", len(long))
	}
}

// QS-14 (#164). Мутация pkg/redact/scanner.go:190-195: цикл по маркерам
// плейсхолдеров удаляется — документация, примеры конфигов и шаблоны начинают
// давать находки, и fail-closed блок экспорта срабатывает на них.
//
// Почему этого не ловил ни один существующий тест: все негативные фикстуры
// (`api_key = "changeme"`, `{"token": "changeme", "api_key": "placeholder"}`)
// короче 16 символов, поэтому отвергались порогом длины на строке 183 либо
// вообще не проходили regexp правила. До цикла по маркерам тест не доходил.
//
// Здесь каждое значение длиннее порога и содержит букву с цифрой, так что оба
// соседних предиката его пропускают. Единственное, что может его отвергнуть, —
// фильтр плейсхолдеров.
func TestPlaceholderFilterIsSoleRejectorForLongTemplateValues(t *testing.T) {
	values := []string{
		"example-token-0123456789",
		"placeholder-secret-42xyz",
		"changeme-0123456789abcd",
		"your-api-key-1234567890",
		"dummy-credential-99887766",
		"sample-token-0123456789",
		"<insert-token-here-1234567890>x",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			if len(value) < 16 {
				t.Fatalf("подготовка теста неверна: %q отсекается порогом длины", value)
			}
			if !hasLetterAndDigit(value) {
				t.Fatalf("подготовка теста неверна: %q отсекается проверкой буква+цифра", value)
			}
			if likelySecretValue(value) {
				t.Fatalf("значение-плейсхолдер %q признано секретом", value)
			}
			// Оба пути сканера: line-based assignment и структурный JSON.
			if findings := Scan([]byte("api_key = " + value + "\n")); len(findings) != 0 {
				t.Fatalf("плейсхолдер в assignment дал находки: %+v", findings)
			}
			if findings := Scan([]byte(`{"api_key": "` + value + `"}`)); len(findings) != 0 {
				t.Fatalf("плейсхолдер в JSON дал находки: %+v", findings)
			}
		})
	}
	// Положительный контроль: значение той же длины и формы, но без маркера,
	// секретом считается — иначе тест проходил бы и при «всё benign».
	real := "kf83jd0192msla72bdq4"
	if !likelySecretValue(real) {
		t.Fatalf("значение %q без маркера должно считаться секретом", real)
	}
	if findings := Scan([]byte("api_key = " + real + "\n")); len(findings) == 0 {
		t.Fatal("настоящий токен в assignment должен давать находку")
	}
}

// hasLetterAndDigit / containsPlaceholderMarker — независимые от
// likelySecretValue копии двух её соседних условий. Нужны, чтобы тесты выше
// могли утверждать: вход НЕ отсекается соседним предикатом, то есть
// проверяемый предикат действительно единственный, кто может его отвергнуть.
func hasLetterAndDigit(value string) bool {
	var letter, digit bool
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return letter && digit
}

func containsPlaceholderMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"example", "placeholder", "changeme", "your-", "dummy", "sample", "<insert"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
