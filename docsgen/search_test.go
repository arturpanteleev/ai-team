package docsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchUsesRenderedFragmentsAndBasePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Начало\n\n## Approval\n\nТочный SHA & проверка.\n\n## Approval\n\nПовторный subject.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := Build(Config{Root: root, Output: out, Title: "Test", BasePath: "/ai-team", Sources: []SourcedPage{{Source: "README.md", Title: "Старт", URL: "/"}}}); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(out, "assets/search-index.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`/ai-team/#approval`, `/ai-team/#approval-1`, `Точный SHA`, `Повторный subject`} {
		if !strings.Contains(string(script), want) {
			t.Errorf("search index missing %q", want)
		}
	}
	if err := CheckLinksWithBase(out, "/ai-team"); err != nil {
		t.Fatal(err)
	}
	// Byte-identical output matters for a static docs build and a stable index.
	before := string(script)
	if err := Build(Config{Root: root, Output: out, Title: "Test", BasePath: "/ai-team", Sources: []SourcedPage{{Source: "README.md", Title: "Старт", URL: "/"}}}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(out, "assets/search-index.js"))
	if before != string(after) {
		t.Fatal("search index is not deterministic")
	}
}
