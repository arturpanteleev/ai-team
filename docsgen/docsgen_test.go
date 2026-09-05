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
		GitHubRepo:  "arturpanteleev/ai-team",
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

	// Non-page assets referenced by relative links must be copied into the
	// output so the relative hrefs resolve on the deployed site.
	for _, want := range []string{
		"LICENSE",                // README badge link
		"contributing/CLAUDE.md", // non-page relative .md
		"demo/ci-gate-demo.yaml", // demo CI asset
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(want))); err != nil {
			t.Errorf("referenced asset not copied to %s: %v", want, err)
		}
	}

	// Directory links are rewritten to GitHub tree URLs instead of 404ing.
	if !strings.Contains(string(idx), "https://github.com/arturpanteleev/ai-team/tree/docsgen") {
		t.Errorf("index.html missing GitHub tree rewrite for docsgen/:\n%.500s", string(idx))
	}

	// The whole generated site must pass the link checker.
	if err := CheckLinks(out); err != nil {
		t.Errorf("CheckLinks failed on generated site: %v", err)
	}
}

// fixtureSite writes the given files (page markdown plus any linked assets)
// beneath a temp root, builds a site from pages, and returns the output dir.
func fixtureSite(t *testing.T, pages []SourcedPage, files map[string]string, dirs []string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	cfg := Config{
		Root:        root,
		Output:      out,
		Title:       "fixture",
		Version:     "dev",
		CleanOutput: true,
		GitHubRepo:  "arturpanteleev/ai-team",
		Sources:     pages,
	}
	if err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return out
}

// TestBuildCopiesReferencedAssets covers the four working-on-GitHub links from
// AUD-12: the LICENSE badge (README), CLAUDE.md (CONTRIBUTING), the demo YAML
// (docs/demo/README) and the docsgen/ directory link.
func TestBuildCopiesReferencedAssets(t *testing.T) {
	out := fixtureSite(t, []SourcedPage{
		{Source: "README.md", Title: "Overview", URL: "/"},
		{Source: "CONTRIBUTING.md", Title: "Contributing", URL: "/contributing/"},
		{Source: "docs/demo/README.md", Title: "Demo", URL: "/demo/"},
	}, map[string]string{
		"README.md":                   "\n[![License](LICENSE)](LICENSE)\n\nSite built by [`docsgen/`](docsgen/).\n",
		"CONTRIBUTING.md":             "See [`CLAUDE.md`](CLAUDE.md).\n",
		"docs/demo/README.md":         "CI: [`ci-gate-demo.yaml`](ci-gate-demo.yaml).\n",
		"LICENSE":                     "Apache-2.0 fixture\n",
		"CLAUDE.md":                   "# agent rules\n",
		"docs/demo/ci-gate-demo.yaml": "name: gate demo\n",
	}, []string{"docsgen"})
	for _, want := range []string{
		"LICENSE",
		filepath.Join("contributing", "CLAUDE.md"),
		filepath.Join("demo", "ci-gate-demo.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(want))); err != nil {
			t.Errorf("copied asset missing at %s: %v", want, err)
		}
	}

	// The docsgen/ directory link must be rewritten to a GitHub tree URL.
	idx, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idx), "https://github.com/arturpanteleev/ai-team/tree/docsgen") {
		t.Errorf("docsgen/ link not rewritten to GitHub tree URL; got:\n%s", string(idx))
	}

	// The ci-gate-demo.yaml relative link must stay relative and resolve.
	demo, err := os.ReadFile(filepath.Join(out, "demo", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(demo), `href="ci-gate-demo.yaml"`) {
		t.Errorf("demo page lost the ci-gate-demo.yaml relative link:\n%s", string(demo))
	}
}

// TestCheckLinksCatchesMissingRelativeLink is the acceptance case from AUD-12:
// if a page links to a missing relative file, the generated-link check fails.
func TestCheckLinksCatchesMissingRelativeLink(t *testing.T) {
	out := fixtureSite(t, []SourcedPage{
		{Source: "README.md", Title: "Overview", URL: "/"},
	}, map[string]string{
		"README.md": "See [`secret-file.yaml`](secret-file.yaml).\n",
	}, nil)
	if err := CheckLinks(out); err == nil {
		t.Fatal("CheckLinks expected to fail for a missing relative link, got nil")
	} else if !strings.Contains(err.Error(), "secret-file.yaml") {
		t.Errorf("CheckLinks error %q does not mention the missing target", err)
	}
}

// TestCheckLinksCatchesDeletedDemoAsset asserts that removing a copied asset
// from the output breaks the generated-link check (the audit's "deletion of
// demo YAML" case).
func TestCheckLinksCatchesDeletedDemoAsset(t *testing.T) {
	out := fixtureSite(t, []SourcedPage{
		{Source: "docs/demo/README.md", Title: "Demo", URL: "/demo/"},
	}, map[string]string{
		"docs/demo/README.md":         "CI: [`ci-gate-demo.yaml`](ci-gate-demo.yaml).\n",
		"docs/demo/ci-gate-demo.yaml": "name: gate demo\n",
	}, nil)
	if err := os.Remove(filepath.Join(out, "demo", "ci-gate-demo.yaml")); err != nil {
		t.Fatalf("remove copied asset: %v", err)
	}
	if err := CheckLinks(out); err == nil {
		t.Fatal("CheckLinks expected to fail after the demo YAML was deleted, got nil")
	}
}

// TestCheckLinksBasePath verifies the checker handles the GitHub Pages base
// path prefix (e.g. /ai-team) used by the deployed site.
func TestCheckLinksBasePath(t *testing.T) {
	out := fixtureSite(t, []SourcedPage{
		{Source: "README.md", Title: "Overview", URL: "/"},
		{Source: "docs/ARCHITECTURE.md", Title: "Architecture", URL: "/architecture/"},
	}, map[string]string{
		"README.md":            "",
		"docs/ARCHITECTURE.md": "",
	}, nil)
	// Simulate what the Pages workflow deploys: every root-relative link in
	// every page carries the /ai-team prefix.
	files, err := listHTML(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range files {
		path := filepath.Join(out, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		patched := strings.ReplaceAll(string(data), `href="/`, `href="/ai-team/`)
		if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckLinksWithBase(out, "/ai-team"); err != nil {
		t.Errorf("CheckLinksWithBase failed with base path: %v", err)
	}
}
