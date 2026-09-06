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

// TestCopyAssetsRejectsPathEscapeFromNestedPage reproduces the F-6 audit's
// repro shape: a page nested two directories deep (docs/demo/README.md,
// rendered at the shortened URL "/demo/", exactly as cmd/docsgen configures
// it) links upward far enough ("../../LICENSE") that the page-URL-relative
// output placement computes a path outside the output directory entirely
// (pageDir "demo" has only one segment, but the link needs to climb two).
// The build must fail loudly instead of writing outside output.
func TestCopyAssetsRejectsPathEscapeFromNestedPage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "demo", "README.md"), []byte("[license](../../LICENSE)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("Apache-2.0 fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	cfg := Config{
		Root:        root,
		Output:      out,
		Title:       "fixture",
		Version:     "dev",
		CleanOutput: true,
		GitHubRepo:  "arturpanteleev/ai-team",
		Sources: []SourcedPage{
			{Source: "docs/demo/README.md", Title: "Demo", URL: "/demo/"},
		},
	}

	err := Build(cfg)
	if err == nil {
		t.Fatal("expected Build to fail for an asset link escaping the output directory")
	}
	for _, want := range []string{"docs/demo/README.md", "LICENSE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The escaping write would have landed one level above output, named
	// LICENSE; confirm it was never created.
	escaped := filepath.Join(filepath.Dir(out), "LICENSE")
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("asset escaped to %s outside the output directory", escaped)
	} else if !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
}

// TestCopyAssetsRejectsPathEscapeOutsideRepo is a second, independent escape
// construction distinct from the nested-page repro above: a top-level page
// links to a file that lives entirely outside the source root (not merely
// outside its own page's subtree). It must be rejected the same way.
func TestCopyAssetsRejectsPathEscapeOutsideRepo(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("[evil](../evil.txt)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "evil.txt"), []byte("outside the repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	cfg := Config{
		Root:        root,
		Output:      out,
		Title:       "fixture",
		Version:     "dev",
		CleanOutput: true,
		Sources: []SourcedPage{
			{Source: "README.md", Title: "Overview", URL: "/"},
		},
	}

	err := Build(cfg)
	if err == nil {
		t.Fatal("expected Build to fail for an asset link escaping the output directory")
	}
	for _, want := range []string{"README.md", "evil.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	escaped := filepath.Join(filepath.Dir(out), "evil.txt")
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("asset escaped to %s outside the output directory", escaped)
	} else if !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
}

// TestSlashRelRepoTargetUsesRepoRoot proves slashRelRepoTarget computes paths
// relative to the actual repository root, not the rendering page's own
// baseDir. Before the fix, a nested page's baseDir (e.g. docs/demo) was
// reused for this purpose, producing a wrong GitHub URL for anything outside
// that page's own subtree — e.g. LICENSE (at the repo root) would resolve to
// "../../LICENSE" instead of "LICENSE".
func TestSlashRelRepoTargetUsesRepoRoot(t *testing.T) {
	root := t.TempDir()
	tr := &linkTransformer{
		baseDir:  filepath.Join(root, "docs", "demo"),
		repoRoot: root,
	}
	got := slashRelRepoTarget(tr, filepath.Join(root, "LICENSE"))
	if got != "LICENSE" {
		t.Errorf("slashRelRepoTarget = %q, want %q", got, "LICENSE")
	}
}

// TestGithubTreeURLFromNestedPageIsRepoRootRelative exercises the repoRoot
// fix through the real rendering pipeline (directory links are the only
// place slashRelRepoTarget currently feeds a GitHub URL): a page nested two
// directories deep links to a directory that lives at the repo root. The
// generated GitHub tree URL must be repo-root-relative ("docsgen"), not
// relative to the page's own directory ("../../docsgen").
func TestGithubTreeURLFromNestedPageIsRepoRootRelative(t *testing.T) {
	out := fixtureSite(t, []SourcedPage{
		{Source: "docs/demo/README.md", Title: "Demo", URL: "/demo/"},
	}, map[string]string{
		"docs/demo/README.md": "See [`docsgen`](../../docsgen/).\n",
	}, []string{"docsgen"})

	demo, err := os.ReadFile(filepath.Join(out, "demo", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(demo), "https://github.com/arturpanteleev/ai-team/tree/docsgen") {
		t.Errorf("expected repo-root-relative GitHub tree URL for docsgen/, got:\n%s", string(demo))
	}
	if strings.Contains(string(demo), "tree/../../docsgen") {
		t.Errorf("GitHub tree URL used page-relative baseDir instead of repo root:\n%s", string(demo))
	}
}
