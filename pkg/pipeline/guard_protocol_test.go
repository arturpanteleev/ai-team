package pipeline

import "testing"

func TestReadOnlyStageAllowsProtocolFilesOnly(t *testing.T) {
	feature := "google-auth"
	protocol := []string{
		"tasks/google-auth/questions.md",
		"google-auth/status/analyst.md",
		"google-auth/.stage-summary/analyst.md",
		".ai-team/artifacts/google-auth/status/analyst.md",
		"google-auth/tasks/google-auth/questions.md",
	}
	for _, path := range protocol {
		if hasProjectChange([]string{path}, feature, "analyst") {
			t.Fatalf("протокольный файл %q не должен считаться мутацией проекта", path)
		}
	}
	if !hasProjectChange([]string{"tasks/google-auth/questions.md", "src/app.go"}, feature, "analyst") {
		t.Fatal("изменение исходника должно нарушать политику read-only этапа")
	}
	if !hasProjectChange([]string{"tasks/other/questions.md"}, feature, "analyst") {
		t.Fatal("вопросы другой feature не являются протокольным файлом этой задачи")
	}
	if hasProjectChange(nil, feature, "analyst") {
		t.Fatal("пустой список изменений не нарушает политику")
	}
}
