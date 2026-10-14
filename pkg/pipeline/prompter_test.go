package pipeline

import "testing"

// The web server must never wait on the process stdin for a human decision:
// its decisions arrive through the API, so the prompter must refuse to ask.
func TestNonInteractivePrompterNeverAsks(t *testing.T) {
	var prompter Prompter = NonInteractivePrompter{}
	if prompter.Interactive() {
		t.Fatal("NonInteractivePrompter must report non-interactive regardless of the attached terminal")
	}
	if answer := prompter.Ask("approve?"); answer == "y" || answer == "" || answer == "approve" {
		t.Fatalf("NonInteractivePrompter must refuse by default, got %q", answer)
	}
}
