package cloudidentity

import (
	"strings"
	"testing"
)

func TestLocalOperatorVerifiesOnlyIssuedToken(t *testing.T) {
	operator, token, err := NewLocalOperator()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < minLocalTokenBytes {
		t.Fatalf("сгенерированный token слишком короткий: %d", len(token))
	}
	principal, err := operator.Verify(token)
	if err != nil {
		t.Fatalf("выданный token отклонён: %v", err)
	}
	if principal.ActorID != LocalOperatorActorID {
		t.Fatalf("actor ID: %q", principal.ActorID)
	}
	for _, role := range AllRoles() {
		if !principal.Has(role) {
			t.Fatalf("локальный оператор не имеет роли %q", role)
		}
	}
	mutated := token[:len(token)-1] + "A"
	if strings.HasSuffix(token, "A") {
		mutated = token[:len(token)-1] + "B"
	}
	for _, wrong := range []string{"", token + "x", mutated} {
		if _, err := operator.Verify(wrong); err == nil {
			t.Fatalf("чужой token %q принят", wrong)
		}
	}
}

func TestLocalOperatorRejectsShortToken(t *testing.T) {
	if _, err := NewLocalOperatorWithToken("short"); err == nil {
		t.Fatal("короткий token должен быть отклонён")
	}
	if _, err := NewLocalOperatorWithToken(strings.Repeat("t", minLocalTokenBytes)); err != nil {
		t.Fatalf("token допустимой длины отклонён: %v", err)
	}
}
