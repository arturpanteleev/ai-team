package scope

import "testing"

func TestMatchAny(t *testing.T) {
	patterns := []string{"**/*_test.go", "tests/**", "**/*.spec.*"}
	tests := map[string]bool{
		"handler_test.go":          true,
		"pkg/api/handler_test.go":  true,
		"tests/fixture/input.json": true,
		"web/button.spec.tsx":      true,
		"pkg/api/handler.go":       false,
	}
	for value, want := range tests {
		if got := MatchAny(patterns, value); got != want {
			t.Errorf("MatchAny(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, invalid := range []string{"", "../outside", "/absolute", `..\\outside`} {
		if err := Validate(invalid); err == nil {
			t.Errorf("Validate(%q) должна вернуть ошибку", invalid)
		}
	}
	if err := Validate("**/*_test.go"); err != nil {
		t.Fatalf("валидный scope: %v", err)
	}
}
