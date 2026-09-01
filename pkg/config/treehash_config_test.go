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
	dd := checks.DefaultIgnoreDirs()
	if !dd["build"] || !dd[".scanner"] {
		t.Fatal("extra ignore dirs должны быть зарегистрированы в canonical tree hash")
	}
	if !dd[".git"] {
		t.Fatal("baseline не должен быть ослаблен")
	}
}

func TestLoadRejectsInvalidTreeHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
schema_version: 4
pipeline: [a]
tree_hash:
  ignore_dirs:
    - foo/bar
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Load не валидирует ignore-имена сам, но применяет их только если валидны;
	// полная строгая валидация — через Config.Validate.
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err == nil {
		t.Error("недопустимый ignore path должен быть отвергнут Validate")
	}
}
