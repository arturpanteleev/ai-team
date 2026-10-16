package demo

import "testing"

func TestBase(t *testing.T) {
	if Base() != 1 {
		t.Fatal("base")
	}
}
