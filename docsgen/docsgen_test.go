package docsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugifyID(t *testing.T) {
	cases := map[string]string{
		"Для кого этот инструмент":        "для-кого-этот-инструмент",
		"CLI-справочник":                  "cli-справочник",
		"Конвейер и зоны ответственности": "конвейер-и-зоны-ответственности",
		"  Leading/trailing  ":            "leading-trailing",
	}
	for in, want := range cases {
		if got := slugifyID(in); got != want {
			t.Errorf("slugifyID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"README.md":            "/",
		"docs/ARCHITECTURE.md": "/docs/ARCHITECTURE/",
		"docs/demo/README.md":  "/docs/demo/README/",
		"CONTRIBUTING.md":      "/CONTRIBUTING/",
		"SECURITY.md":          "/SECURITY/",
		"CHANGELOG.md":         "/CHANGELOG/",
		"CODE_OF_CONDUCT.md":   "/CODE_OF_CONDUCT/",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildTOCAnchorsMatchHeadingIDs(t *testing.T) {
	content := []byte("# Top\n\n## Граница безопасности\n\nsome text\n\n### integrity vs authenticity\n\nmore\n\n## Разработка\n")
	toc := buildTOC(content)
	// The TOC should include anchors for the h2/h3 headings.
	for _, anchor := range []string{
		"#граница-безопасности",
		"#integrity-vs-authenticity",
		"#разработка",
	} {
		if !strings.Contains(toc, anchor) {
			t.Errorf("TOC missing anchor %q; got:\n%s", anchor, toc)
		}
	}
}

func TestBuildTOCSkipsH1AndEmpty(t *testing.T) {
	if got := buildTOC([]byte("# Only h1\n")); got != "" {
		t.Errorf("expected empty TOC for h1-only, got %q", got)
	}
}

func TestSortPagesBySectionAndWeight(t *testing.T) {
	pages := []*Page{
		{Title: "Changelog", Section: "Project", Weight: 0, URL: "/changelog/"},
		{Title: "Overview", Section: "Guide", Weight: 0, URL: "/"},
		{Title: "Security", Section: "Community", Weight: 1, URL: "/security/"},
		{Title: "Contributing", Section: "Community", Weight: 0, URL: "/contributing/"},
	}
	sortPages(pages)
	var order []string
	for _, p := range pages {
		order = append(order, p.Title)
	}
	want := []string{"Overview", "Contributing", "Security", "Changelog"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("sort order = %v, want %v", order, want)
	}
}

// TestBuildEndToEnd runs a full site build against the real repo sources and
// asserts the expected output files exist and generated links resolve to pages.
func TestBuildEndToEnd(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// os.Getwd is the package dir (docsgen/); repo root is one level up.
	repoRoot := filepath.Dir(root)
	out := t.TempDir()

	cfg := Config{
		Root:        repoRoot,
		Output:      out,
		Title:       "ai-team",
		Version:     "dev",
		CleanOutput: true,
		Sources: []SourcedPage{
			{Source: "README.md", Title: "Overview", Section: "Guide", Weight: 0, URL: "/"},
			{Source: "docs/ARCHITECTURE.md", Title: "Architecture", Section: "Reference", Weight: 0, URL: "/architecture/"},
			{Source: "CONTRIBUTING.md", Title: "Contributing", Section: "Community", Weight: 0, URL: "/contributing/"},
			{Source: "SECURITY.md", Title: "Security", Section: "Community", Weight: 1, URL: "/security/"},
			{Source: "CODE_OF_CONDUCT.md", Title: "Code of Conduct", Section: "Community", Weight: 2, URL: "/code-of-conduct/"},
			{Source: "CHANGELOG.md", Title: "Changelog", Section: "Project", Weight: 0, URL: "/changelog/"},
			{Source: "docs/demo/README.md", Title: "Demo", Section: "Reference", Weight: 1, URL: "/demo/"},
		},
	}

	if err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, want := range []string{
		"index.html",
		"architecture/index.html",
		"contributing/index.html",
		"security/index.html",
		"code-of-conduct/index.html",
		"changelog/index.html",
		"demo/index.html",
		"assets/site.css",
	} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing output file %s: %v", want, err)
		}
	}

	// Cross-links to other markdown sources must be rewritten to site pages.
	idx, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`href="/architecture/"`,
		`href="/contributing/"`,
		`href="/security/"`,
	} {
		if !strings.Contains(string(idx), want) {
			t.Errorf("index.html missing rewritten link %s", want)
		}
	}
	// Raw markdown links must not remain as broken .md hrefs.
	if strings.Contains(string(idx), `.md"`) {
		t.Errorf("index.html still contains raw .md links:\n%#v", string(idx))
	}
}
