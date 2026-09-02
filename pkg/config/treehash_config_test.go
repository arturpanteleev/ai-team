package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
)

func TestTreeHashConfigValidate(t *testing.T) {
	// валидный
	valid := &TreeHashConfig{IgnoreDirs: []string{"coverage", ".cache"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("ожидали valid: %v", err)
	}
	// недопустимые имена
	for _, name := range []string{"", ".", "..", "a/b", "-x", "a/b/c", "x y"} {
		tc := &TreeHashConfig{IgnoreDirs: []string{name}}
		if err := tc.Validate(); err == nil {
			t.Errorf("name %q должен быть отвергнут", name)
		}
	}
	// дубликаты
	dup := &TreeHashConfig{IgnoreDirs: []string{"coverage", "coverage"}}
	if err := dup.Validate(); err == nil {
		t.Error("дубликат должен быть отвергнут")
	}
}

func TestLoadAppliesTreeHashIgnoreDirs(t *testing.T) {
	checks.ResetExtraIgnoreDirs()
	defer checks.ResetExtraIgnoreDirs()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
schema_version: 4
pipeline: [a]
workflow:
  entry: a
  max_visits: {a: 1}
  edges:
    - from: a
      outcome: passed
      to: $complete
tree_hash:
  ignore_dirs:
    - build
    - .scanner
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.TreeHash == nil || len(cfg.TreeHash.IgnoreDirs) != 2 {
		t.Fatalf("tree_hash не загружен: %+v", cfg.TreeHash)
	}
	// Load не регистрирует ignore dirs — это делает только Validate после
	// строгой проверки имени.
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("validate: %v", err)
	}
	dd := checks.DefaultIgnoreDirs()
	if !dd["build"] || !dd[".scanner"] {
		t.Fatal("extra ignore dirs должны быть зарегистрированы в canonical tree hash")
	}
	if !dd[".git"] {
		t.Fatal("baseline не должен быть ослаблен")
	}
}

func TestLoadRejectsInvalidTreeHash(t *testing.T) {
	checks.ResetExtraIgnoreDirs()
	defer checks.ResetExtraIgnoreDirs()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
schema_version: 4
pipeline: [a]
workflow:
  entry: a
  max_visits: {a: 1}
  edges:
    - from: a
      outcome: passed
      to: $complete
tree_hash:
  ignore_dirs:
    - foo/bar
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Load сам не валидирует ignore-имена; строгая валидация — через
	// Config.Validate, которая при невалидном имени НЕ регистрирует
	// «foo/bar» в процесс-глобальном ignore-list.
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err == nil {
		t.Error("недопустимый ignore path должен быть отвергнут Validate")
	}
	if dd := checks.DefaultIgnoreDirs(); dd["foo/bar"] {
		t.Error("невалидное имя не должно попасть в процесс-глобальный ignore-list")
	}
}
