package workflow

import "testing"

func TestValidFeature(t *testing.T) {
	for _, valid := range []string{"feature-1", "task_2", "a.b"} {
		if !ValidFeature(valid) {
			t.Errorf("valid feature rejected: %q", valid)
		}
	}
	for _, invalid := range []string{"", ".", "..", "a..b", "../escape", "a/b", "a\\b", "задача", ".hidden", "x.lock", "x."} {
		if ValidFeature(invalid) {
			t.Errorf("invalid feature accepted: %q", invalid)
		}
	}
}
