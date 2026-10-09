package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

func TestInterspersedStageSubmitArgs(t *testing.T) {
	got := interspersedStageSubmitArgs([]string{
		"run-42", "--stage", "product_spec", "--md", "./spec.md", "--description=ready", "--target", ".",
	})
	want := []string{
		"--stage", "product_spec", "--md", "./spec.md", "--description=ready", "--target", ".", "run-42",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interspersed arguments = %#v, want %#v", got, want)
	}

	got = interspersedStageSubmitArgs([]string{"--approve", "run-42", "--stage=review", "--note", "looks good"})
	want = []string{"--approve", "--stage=review", "--note", "looks good", "run-42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equals and boolean flags = %#v, want %#v", got, want)
	}
}

func TestOpenStageApprovalStoreSelectsSafeLocalOrSQLiteStore(t *testing.T) {
	t.Run("local file store by default", func(t *testing.T) {
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0755); err != nil {
			t.Fatal(err)
		}
		store, closeStore, err := openStageApprovalStore(target, "")
		if err != nil || closeStore != nil {
			t.Fatalf("open local approval store: store=%T close=%v err=%v", store, closeStore != nil, err)
		}
		if _, ok := store.(*approval.Store); !ok {
			t.Fatalf("default approval store = %T, want *approval.Store", store)
		}
		if _, err := store.List("run-1"); err != nil {
			t.Fatalf("list local approvals: %v", err)
		}
	})

	t.Run("automatically use web SQLite database", func(t *testing.T) {
		target := t.TempDir()
		controlRoot := filepath.Join(target, ".ai-team")
		if err := os.Mkdir(controlRoot, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(controlRoot, "web.db")
		created, err := approval.NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		store, closeStore, err := openStageApprovalStore(target, "")
		if err != nil || closeStore == nil {
			t.Fatalf("open SQLite approval store: store=%T close=%v err=%v", store, closeStore != nil, err)
		}
		if _, ok := store.(*approval.SQLiteStore); !ok {
			t.Fatalf("auto-selected approval store = %T, want *approval.SQLiteStore", store)
		}
		if _, err := store.List("run-1"); err != nil {
			t.Fatalf("list SQLite approvals: %v", err)
		}
		if err := closeStore(); err != nil {
			t.Fatalf("close SQLite approvals: %v", err)
		}
	})

	t.Run("reject database outside control root", func(t *testing.T) {
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openStageApprovalStore(target, filepath.Join(t.TempDir(), "outside.db")); err == nil {
			t.Fatal("accepted SQLite database outside .ai-team")
		}
	})
}
