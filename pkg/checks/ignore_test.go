package checks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidIgnoreDirNameStrict(t *testing.T) {
	valid := []string{"coverage", ".cache", "target", "build-out", ".next"}
	for _, name := range valid {
		if !ValidIgnoreDirName(name) {
			t.Errorf("ожидали валидное имя %q", name)
		}
	}
	invalid := map[string]bool{
		"": true, ".": true, "..": true,
		"a/b": true, "a\\b": true, "/abs": true,
		"-leading": true, "has space": true, "quo\"te": true,
		"non\x00ascii": true,
	}
	for name := range invalid {
		if ValidIgnoreDirName(name) {
			t.Errorf("%q: получили valid, ожидали invalid", name)
		}
	}
	if ValidIgnoreDirName(string(make([]byte, 129))) {
		t.Error("слишком длинное имя должно быть недопустимо")
	}
}

func TestExtraIgnoreDirsMergeAndDigest(t *testing.T) {
	ResetExtraIgnoreDirs()
	defer ResetExtraIgnoreDirs()

	dir := t.TempDir()
	mkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(dir, rel), 0755); err != nil {
			t.Fatal(err)
		}
	}
	mkfile := func(rel string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(rel), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("a.txt")
	mkdir("coverage")
	mkfile("coverage/x.txt")
	mkdir("src")
	mkfile("src/main.go")

	baseDigest, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}

	// project-specific ignore: coverage
	SetExtraIgnoreDirs([]string{"coverage"})
	withExtra, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if withExtra == baseDigest {
		t.Fatal("digest должен измениться при добавлении ignore-имени")
	}

	// canonical baseline никогда не удаляется: .git остаётся в DefaultIgnoreDirs
	dd := DefaultIgnoreDirs()
	if !dd[".git"] || !dd[".ai-team"] {
		t.Fatal("baseline должен оставаться")
	}
	if !dd["coverage"] {
		t.Fatal("extra ignore должен быть добавлен")
	}

	ResetExtraIgnoreDirs()
	afterReset, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if afterReset != baseDigest {
		t.Fatal("reset должен вернуть baseline digest")
	}
}
