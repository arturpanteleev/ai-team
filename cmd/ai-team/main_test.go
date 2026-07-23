package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckControlRootDistinguishesUninitializedFromUnsafe(t *testing.T) {
	t.Run("не инициализирован", func(t *testing.T) {
		target := t.TempDir()
		err := checkControlRoot(target)
		if err == nil || !strings.Contains(err.Error(), "не инициализирован") {
			t.Fatalf("ожидалось сообщение про неинициализированный проект, получено: %v", err)
		}
		if strings.Contains(err.Error(), "небезопасный") {
			t.Fatalf("сообщение о неинициализированном проекте не должно упоминать небезопасность: %v", err)
		}
	})

	t.Run("небезопасен (symlink)", func(t *testing.T) {
		target := t.TempDir()
		realDir := filepath.Join(target, "elsewhere")
		if err := os.Mkdir(realDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, filepath.Join(target, ".ai-team")); err != nil {
			t.Fatal(err)
		}
		err := checkControlRoot(target)
		if err == nil || !strings.Contains(err.Error(), "небезопасный") {
			t.Fatalf("ожидалось сообщение про небезопасный control root, получено: %v", err)
		}
		if strings.Contains(err.Error(), "не инициализирован") {
			t.Fatalf("сообщение о небезопасном control root не должно звучать как «не инициализирован»: %v", err)
		}
	})

	t.Run("валидный control root", func(t *testing.T) {
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := checkControlRoot(target); err != nil {
			t.Fatalf("валидный control root не должен возвращать ошибку: %v", err)
		}
	})
}
