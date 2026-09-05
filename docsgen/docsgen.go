// Package docsgen builds a static documentation site from the repository's
// Markdown sources (README, docs/, CONTRIBUTING, SECURITY, CHANGELOG, ...).
//
// The Markdown files remain the authoritative, agent-friendly source of
// truth; docsgen turns them into a self-contained, browseable HTML site for
// humans. It is deterministic: identical Markdown in produces byte-identical
// output, which keeps the build reproducible and testable.
package docsgen

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// SourcedPage declares an input Markdown file and how it maps to a page.
type SourcedPage struct {
	Source  string
	Title   string
	Section string
	Weight  int
	// URL overrides the computed output URL (default derived from Source).
	// Use "/" to make a page the site index.
	URL string
}

// Page is a single documentation page derived from one Markdown file.
type Page struct {
	Source  string
	Title   string
	Section string
	Weight  int
	URL     string

	Body template.HTML
	TOC  template.HTML
}

// Site holds the full set of pages and shared metadata used by the layout.
type Site struct {
	Title    string
	Version  string
	BasePath string
	Pages    []*Page
	Current  *Page
}

// Config describes the whole site build.
type Config struct {
	Root        string
	Output      string
	Title       string
	Version     string
	BasePath    string
	CleanOutput bool
	Sources     []SourcedPage
	// GitHubRepo is the "owner/repo" used to rewrite directory links to
	// GitHub blob URLs (e.g. "arturpanteleev/ai-team").
	GitHubRepo string
}

// Build renders all configured Markdown sources into HTML under Output.
func Build(cfg Config) error {
	if cfg.CleanOutput && cfg.Output != "" {
		if err := os.RemoveAll(cfg.Output); err != nil {
			return fmt.Errorf("clean output: %w", err)
		}
	}

	site := &Site{
		Title:    cfg.Title,
		Version:  cfg.Version,
		BasePath: normalizeBasePath(cfg.BasePath),
	}

	// Path map: repository-absolute Markdown path -> site URL, used to rewrite
	// cross-links between Markdown files to the generated pages.
	pathMap := make(map[string]string)
	for _, sp := range cfg.Sources {
		abs, err := filepath.Abs(filepath.Join(cfg.Root, filepath.FromSlash(sp.Source)))
		if err != nil {
			return err
		}
		url := sp.URL
		if url == "" {
			url = slugify(sp.Source)
		}
		pathMap[abs] = joinBase(site.BasePath, url)
	}

	var nonPage []nonPageLink
	for _, sp := range cfg.Sources {
		srcPath := filepath.Join(cfg.Root, filepath.FromSlash(sp.Source))

		absSrc, err := filepath.Abs(srcPath)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", sp.Source, err)
		}
		content, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read source %s: %w", sp.Source, err)
		}
		url := sp.URL
		if url == "" {
			url = slugify(sp.Source)
		}
		tr := &linkTransformer{
			baseDir:    filepath.Dir(absSrc),
			pathMap:    pathMap,
			pageURL:    url,
			githubRepo: cfg.GitHubRepo,
		}
		page, err := renderPage(tr, sp, content)
		if err != nil {
			return fmt.Errorf("render %s: %w", sp.Source, err)
		}
		nonPage = append(nonPage, tr.nonPageLinks...)
		site.Pages = append(site.Pages, page)
	}

	sortPages(site.Pages)

	if err := writeLayoutAssets(cfg.Output); err != nil {
		return err
	}

	if err := copyAssets(dedupeNonPage(nonPage), cfg.Output); err != nil {
		return err
	}

	for _, page := range site.Pages {
		site.Current = page
		htmlOut, err := executeLayout(site)
		if err != nil {
			return fmt.Errorf("layout %s: %w", page.Source, err)
		}
		if page.URL == "/" {
			if err := os.WriteFile(filepath.Join(cfg.Output, "index.html"), htmlOut, 0o644); err != nil {
				return fmt.Errorf("write index: %w", err)
			}
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(page.URL, "/"), "/")
		outDir := filepath.Join(cfg.Output, filepath.FromSlash(rel))
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", outDir, err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "index.html"), htmlOut, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outDir, err)
		}
	}

	return nil
}

// renderPage converts Markdown bytes into a Page with a body and TOC.
// tr carries the transformation configuration (baseDir, pathMap, pageURL and
// githubRepo) and accumulates any non-page relative links it finds.
func renderPage(tr *linkTransformer, sp SourcedPage, content []byte) (*Page, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Table,
			extension.Linkify,
		),
		goldmark.WithParserOptions(
			parser.WithASTTransformers(
				util.Prioritized(tr, 100),
				util.Prioritized(&headingIDTransformer{}, 200),
			),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)

	var buf bytes.Buffer
	if err := md.Convert(content, &buf); err != nil {
		return nil, err
	}

	title := sp.Title
	if title == "" {
		title = firstHeading(content)
	}
	if title == "" {
		title = sp.Source
	}

	url := sp.URL
	if url == "" {
		url = slugify(sp.Source)
	}

	return &Page{
		Source:  sp.Source,
		Title:   title,
		Section: sp.Section,
		Weight:  sp.Weight,
		URL:     url,
		Body:    template.HTML(buf.String()),
		TOC:     template.HTML(buildTOC(content)),
	}, nil
}

// headingIDTransformer assigns readable, stable IDs to headings so that the
// in-page TOC (built by the same slugifyID) stays in sync with goldmark.
// It replaces goldmark's default auto-heading-ID, which drops Cyrillic.
type headingIDTransformer struct {
	seen map[string]int
}

func (t *headingIDTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	if t.seen == nil {
		t.seen = make(map[string]int)
	}
	ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		base := slugifyID(string(h.Text(source)))
		if base == "" {
			return ast.WalkContinue, nil
		}
		if t.seen[base] == 0 {
			h.SetAttributeString("id", []byte(base))
		} else {
			h.SetAttributeString("id", []byte(fmt.Sprintf("%s-%d", base, t.seen[base])))
		}
		t.seen[base]++
		return ast.WalkContinue, nil
	})
}

// slugifyID converts heading text into a stable URL fragment. Unicode letters
// (including Cyrillic) are preserved so Russian headings get readable anchors;
// spaces and punctuation become hyphens.
func slugifyID(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.TrimSpace(strings.ToLower(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// linkTransformer rewrites Markdown cross-links that point at other .md files
// in the repository so they resolve to the generated site pages instead of
// missing raw files. It also collects non-page relative links (assets,
// non-page markdown files) for copying into the output directory.
type linkTransformer struct {
	baseDir      string
	pathMap      map[string]string
	pageURL      string // site URL of the page being rendered (e.g. "/contributing/")
	githubRepo   string
	nonPageLinks []nonPageLink
}

// nonPageLink tracks a relative link to a repository file that is not among
// the rendered pages and needs to be copied into the site output.
type nonPageLink struct {
	resolved string // absolute repo path of the target file
	outPath  string // relative path within the output directory
}

func (t *linkTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest []byte
		switch n := n.(type) {
		case *ast.Link:
			dest = n.Destination
		case *ast.Image:
			dest = n.Destination
		default:
			return ast.WalkContinue, nil
		}
		if len(dest) == 0 {
			return ast.WalkContinue, nil
		}
		rewritten, ok := t.rewrite(string(dest))
		if !ok {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			n.Destination = []byte(rewritten)
		case *ast.Image:
			n.Destination = []byte(rewritten)
		}
		return ast.WalkContinue, nil
	})
}

func (t *linkTransformer) rewrite(dest string) (string, bool) {
	// Split off the fragment so non-page assets keep their anchors.
	idx := strings.Index(dest, "#")
	pathPart := dest
	fragment := ""
	if idx >= 0 {
		pathPart = dest[:idx]
		fragment = dest[idx:]
	}
	// Skip absolute/external URLs and anchor-only links.
	if strings.HasPrefix(pathPart, "http://") || strings.HasPrefix(pathPart, "https://") ||
		strings.HasPrefix(pathPart, "//") || strings.HasPrefix(pathPart, "mailto:") {
		return "", false
	}
	if pathPart == "" {
		return "", false
	}

	// Links to repository Markdown pages are mapped to generated pages.
	if strings.HasSuffix(pathPart, ".md") || strings.HasSuffix(pathPart, ".md/") {
		resolved := filepath.Clean(filepath.Join(t.baseDir, filepath.FromSlash(strings.TrimPrefix(pathPart, "/"))))
		target, ok := t.pathMap[resolved]
		if !ok {
			// A .md file that is not a rendered page: copy it if it exists so
			// the relative link still resolves on the site.
			if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
				t.nonPageLinks = append(t.nonPageLinks, t.nonPageLinkFor(resolved))
			}
			// Unknown/absent target: leave as-is (may be an anchor or external).
			return "", false
		}
		return target + fragment, true
	}

	// Directory links (e.g. "docsgen/") resolve to GitHub tree URLs so they
	// don't 404 on the site.
	dirCandidate := filepath.Join(t.baseDir, filepath.FromSlash(strings.TrimSuffix(strings.TrimPrefix(pathPart, "/"), "/")))
	if info, err := os.Stat(dirCandidate); err == nil && info.IsDir() && t.githubRepo != "" {
		return "https://github.com/" + t.githubRepo + "/tree/" + slashRelRepoTarget(t, dirCandidate) + fragment, true
	}

	// Non-page files (assets, LICENSE, YAML, ...) are copied into the output
	// directory so the relative link resolves without a GitHub round-trip.
	resolved := filepath.Clean(filepath.Join(t.baseDir, filepath.FromSlash(strings.TrimPrefix(pathPart, "/"))))
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		t.nonPageLinks = append(t.nonPageLinks, t.nonPageLinkFor(resolved))
	}
	// Leave the link as-is: the copied file matches the original relative path.
	return "", false
}

// nonPageLinkFor computes the output path for a resolved repo file. The page
// renders at pageURL (e.g. "/contributing/"), so a relative link resolves at
// the page's output directory depth.
func (t *linkTransformer) nonPageLinkFor(resolved string) nonPageLink {
	relDir, err := filepath.Rel(t.baseDir, filepath.Dir(resolved))
	if err != nil || relDir == "." {
		relDir = ""
	}
	return nonPageLink{
		resolved: resolved,
		outPath:  filepath.Join(filepath.FromSlash(strings.Trim(strings.TrimPrefix(t.pageURL, "/"), "/")), relDir, filepath.Base(resolved)),
	}
}

// slashRelRepoTarget computes the repo-root-relative path of a resolved file,
// used to build GitHub blob/tree URLs.
func slashRelRepoTarget(t *linkTransformer, resolved string) string {
	rel, err := filepath.Rel(t.baseDir, resolved)
	if err != nil {
		return filepath.Base(resolved)
	}
	return filepath.ToSlash(rel)
}

// dedupeNonPage drops duplicate copy targets (the same file linked from more
// than one page), keeping the first occurrence. Copying is idempotent, so this
// only avoids redundant stat/write work.
func dedupeNonPage(links []nonPageLink) []nonPageLink {
	seen := make(map[string]bool, len(links))
	out := links[:0]
	for _, l := range links {
		key := l.outPath
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	return out
}

// copyAssets copies repository files referenced by relative links into the
// matching locations of the output directory so the links resolve on the
// deployed site.
func copyAssets(links []nonPageLink, output string) error {
	for _, l := range links {
		outPath := filepath.Join(output, filepath.FromSlash(l.outPath))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("mkdir asset %s: %w", outPath, err)
		}
		data, err := os.ReadFile(l.resolved)
		if err != nil {
			return fmt.Errorf("read asset %s: %w", l.resolved, err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			return fmt.Errorf("write asset %s: %w", outPath, err)
		}
	}
	return nil
}

// CheckLinks is CheckLinksWithBase with an empty base path (site hosted at "/").
func CheckLinks(output string) error {
	return CheckLinksWithBase(output, "")
}

// CheckLinksWithBase validates that every internal href/src in the generated
// site resolves to an existing output file and that fragments point to an
// element present in the target page. basePath is the site base path (e.g.
// "/ai-team") used by the rendered URLs and stripped before matching against
// the output file tree. Fragment-only, mailto: and absolute (http://, https://,
// //) URLs are skipped. It returns a human-readable error listing the broken
// links, or nil when the site is fully linked.
//
// CheckLinksWithBase deliberately scans the rendered HTML files, not the
// Markdown sources, so it catches both raw links and links produced by layout
// templates.
func CheckLinksWithBase(output, basePath string) error {
	if output == "" {
		return fmt.Errorf("output directory is empty")
	}
	base := normalizeBasePath(basePath)
	files, err := listHTML(output)
	if err != nil {
		return err
	}
	content := make(map[string]string, len(files))
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(output, rel))
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		content[rel] = string(data)
	}

	dirWithIndex := func(d string) bool {
		_, ok := content[filepath.ToSlash(filepath.Join(d, "index.html"))]
		return ok
	}
	// resolveFile maps a URL path from page pageRel to a concrete output file
	// path (empty when nothing matches). Root-relative URLs (leading "/") are
	// resolved against the site root; page-relative ones against the page's
	// output directory.
	resolveFile := func(pageRel, pathPart string) string {
		p := pathPart
		if base != "" {
			if p == base {
				p = "/"
			} else if strings.HasPrefix(p, base+"/") {
				p = p[len(base):]
			}
		}
		rootRel := strings.HasPrefix(p, "/")
		baseDir := ""
		if !rootRel {
			baseDir = pageRel
		}
		target := strings.Trim(strings.TrimPrefix(p, "/"), "/")
		candidate := filepath.ToSlash(filepath.Join(baseDir, filepath.FromSlash(target)))
		if _, ok := content[candidate]; ok {
			return candidate
		}
		if dirWithIndex(candidate) {
			return filepath.ToSlash(filepath.Join(candidate, "index.html"))
		}
		if info, err := os.Stat(filepath.Join(output, candidate)); err == nil && !info.IsDir() {
			return candidate
		}
		return ""
	}

	seen := make(map[string]string) // url -> first page that reported it
	report := func(url, from string) {
		if seen[url] == "" {
			seen[url] = from
		}
	}
	idRe := regexp.MustCompile(`id="([^"]*)"`)

	for _, rel := range files {
		pageDir := filepath.ToSlash(filepath.Dir(rel))
		if pageDir == "." {
			pageDir = ""
		}
		for _, m := range hrefRe.FindAllStringSubmatch(content[rel], -1) {
			url := m[1]
			if url == "" || !needsCheck(url) {
				continue
			}
			pathPart, frag := url, ""
			if i := strings.Index(url, "#"); i >= 0 {
				pathPart, frag = url[:i], url[i+1:]
			}
			if frag != "" {
				frag = unescapeFragment(frag)
			}
			if pathPart != "" {
				targetFile := resolveFile(pageDir, pathPart)
				if targetFile == "" {
					report(url, rel)
					continue
				}
				if frag != "" {
					if ids := idRe.FindAllStringSubmatch(content[targetFile], -1); !hasID(ids, frag) {
						report(url, rel)
					}
				}
				continue
			}
			// Fragment-only link must target a heading in the same page.
			if frag != "" {
				if ids := idRe.FindAllStringSubmatch(content[rel], -1); !hasID(ids, frag) {
					report(url, rel)
				}
			}
		}
	}

	if len(seen) == 0 {
		return nil
	}
	unique := make([]string, 0, len(seen))
	for url := range seen {
		unique = append(unique, url)
	}
	sort.Strings(unique)
	var b strings.Builder
	fmt.Fprintf(&b, "%d broken link(s) in generated site:\n", len(unique))
	for _, url := range unique {
		fmt.Fprintf(&b, "  %s (linked from %s)\n", url, seen[url])
	}
	return fmt.Errorf("%s", strings.TrimSpace(b.String()))
}

// hasID reports whether frag appears among the id attributes extracted from a
// page.
func hasID(ids [][]string, frag string) bool {
	for _, m := range ids {
		if m[1] == frag {
			return true
		}
	}
	return false
}

// unescapeFragment decodes percent-encoded bytes in a URL fragment (goldmark
// encodes non-ASCII hrefs) so it can be compared with raw id attributes.
func unescapeFragment(frag string) string {
	if !strings.Contains(frag, "%") {
		return frag
	}
	if decoded, err := url.PathUnescape(frag); err == nil {
		return decoded
	}
	return frag
}

// hrefRe matches href/src attributes on anchors and images.
var hrefRe = regexp.MustCompile(`(?:href|src)="([^"]*)"`)

// needsCheck reports whether a URL is an internal link that must resolve on
// the generated site (skips fragments, external and protocol-relative URLs).
func needsCheck(url string) bool {
	if strings.HasPrefix(url, "#") {
		return false
	}
	return !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") ||
		strings.HasPrefix(url, "//") || strings.HasPrefix(url, "mailto:") ||
		strings.HasPrefix(url, "tel:") || strings.HasPrefix(url, "data:"))
}

// listHTML returns all HTML files under dir with forward-slash paths relative
// to dir.
func listHTML(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".html" {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files, err
}

// firstHeading extracts the first ATX heading (# Foo) from Markdown bytes.
func firstHeading(content []byte) string {
	for _, line := range bytes.Split(content, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return ""
}

// buildTOC scans Markdown headings (levels 2-3) and emits a nested list whose
// anchors match the IDs goldmark generates (slugified heading text). This
// mirrors the site's auto-generated heading IDs deterministically.
func buildTOC(content []byte) string {
	type item struct {
		level int
		text  string
		id    string
	}
	var items []item
	seen := make(map[string]int)

	for _, rawLine := range bytes.Split(content, []byte("\n")) {
		line := strings.TrimSpace(string(rawLine))
		if !strings.HasPrefix(line, "#") {
			continue
		}
		// Count leading '#'.
		level := 0
		for level < len(line) && line[level] == '#' {
			level++
		}
		rest := strings.TrimSpace(line[level:])
		if rest == "" || level < 2 || level > 3 {
			continue
		}
		base := slugifyID(rest)
		n := seen[base]
		seen[base]++
		id := base
		if n > 0 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		items = append(items, item{level: level, text: rest, id: id})
	}

	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`<nav class="toc"><strong>On this page</strong><ul>`)
	currentLevel := 2
	openLevel := 0
	for _, it := range items {
		if it.level > currentLevel {
			b.WriteString("<ul>")
			openLevel++
		} else if it.level < currentLevel {
			b.WriteString("</ul>")
			openLevel--
		}
		currentLevel = it.level
		b.WriteString(fmt.Sprintf(`<li><a href="#%s">%s</a></li>`,
			it.id, template.HTMLEscapeString(it.text)))
	}
	for openLevel > 0 {
		b.WriteString("</ul>")
		openLevel--
	}
	b.WriteString("</ul></nav>")
	return b.String()
}

// slugify converts a file source to a default output URL. README.md maps to
// the site root; otherwise the source path (minus ".md") becomes a directory
// with a trailing slash.
func slugify(source string) string {
	s := strings.TrimSuffix(source, ".md")
	s = strings.ReplaceAll(s, `\`, "/")
	s = strings.Trim(s, "/")
	if s == "README" {
		return "/"
	}
	return "/" + s + "/"
}

// normalizeBasePath ensures a base path looks like "" or "/prefix".
func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	return "/" + strings.Trim(p, "/")
}

// joinBase prefixes a root-relative URL with a site base path. If base is
// empty the URL is returned unchanged.
func joinBase(base, url string) string {
	if base == "" || url == "" {
		return url
	}
	return base + url
}

func sortPages(pages []*Page) {
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Section != pages[j].Section {
			return sectionRank(pages[i].Section) < sectionRank(pages[j].Section)
		}
		return pages[i].Weight < pages[j].Weight
	})
}

func sectionRank(s string) int {
	switch s {
	case "Guide":
		return 0
	case "Reference":
		return 1
	case "Community":
		return 2
	case "Project":
		return 3
	default:
		return 99
	}
}

// layoutSrc is the shared HTML shell every page is rendered into. It uses the
// "link" template function so all root-relative hrefs honour the site base path.
const layoutSrc = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Current.Title}} — {{.Title}}</title>
<meta name="generator" content="ai-team docsgen">
<link rel="stylesheet" href="{{link "/assets/site.css"}}">
</head>
<body>
<header class="site-header">
  <a class="brand" href="{{link "/"}}">{{.Title}}</a>
  <span class="version">{{.Version}}</span>
  <nav class="topnav">
    {{range .Pages}}<a href="{{link .URL}}"{{if eq .URL $.Current.URL}} class="active"{{end}}>{{.Title}}</a>{{end}}
  </nav>
</header>
<div class="layout">
  <aside class="sidebar">
    {{template "sidebar" .}}
  </aside>
  <main class="content">
    {{.Current.TOC}}
    <article class="markdown">{{.Current.Body}}</article>
  </main>
</div>
<footer class="site-footer">Generated from Markdown by ai-team docsgen.</footer>
</body>
</html>
`

const sidebarSrc = `
{{- $cur := .Current -}}
{{- $lastSection := "" -}}
{{- range .Pages -}}
  {{- if ne .Section $lastSection -}}
    {{- if ne $lastSection "" -}}</ul>{{- end -}}
    {{- $lastSection = .Section -}}
    <div class="section-label">{{.Section}}</div><ul class="sidenav">
  {{- end -}}
  <li><a href="{{link .URL}}"{{if eq .URL $cur.URL}} class="active"{{end}}>{{.Title}}</a></li>
{{- end -}}
{{- if ne $lastSection "" -}}</ul>{{- end -}}
`

func executeLayout(site *Site) ([]byte, error) {
	funcs := template.FuncMap{
		"link": func(url string) string {
			return joinBase(site.BasePath, url)
		},
	}
	layout, err := template.New("layout").Funcs(funcs).Parse(layoutSrc)
	if err != nil {
		return nil, err
	}
	sidebar, err := template.New("sidebar").Funcs(funcs).Parse(sidebarSrc)
	if err != nil {
		return nil, err
	}
	if _, err := layout.AddParseTree("sidebar", sidebar.Tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := layout.ExecuteTemplate(&buf, "layout", site); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeLayoutAssets(output string) error {
	if err := os.MkdirAll(filepath.Join(output, "assets"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "assets", "site.css"), []byte(css), 0o644)
}

const css = `:root {
  --bg: #ffffff; --fg: #1f2328; --muted: #57606a; --border: #d0d7de;
  --accent: #0969da; --code-bg: #f6f8fa;
}
* { box-sizing: border-box; }
body {
  margin: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
  color: var(--fg); background: var(--bg); line-height: 1.6;
}
.site-header {
  position: sticky; top: 0; z-index: 10; display: flex; align-items: center; gap: 1rem;
  padding: 0.75rem 1.5rem; background: var(--bg); border-bottom: 1px solid var(--border);
}
.brand { font-weight: 600; font-size: 1.1rem; color: var(--fg); text-decoration: none; }
.version { color: var(--muted); font-size: 0.8rem; }
.topnav { margin-left: auto; display: flex; gap: 1rem; flex-wrap: wrap; }
.topnav a { color: var(--muted); text-decoration: none; font-size: 0.9rem; }
.topnav a.active { color: var(--accent); font-weight: 600; }
.layout { display: flex; max-width: 1200px; margin: 0 auto; }
.sidebar {
  width: 260px; min-width: 260px; padding: 1.5rem 1rem 2rem 1.5rem;
  border-right: 1px solid var(--border); height: 100vh; position: sticky; top: 3rem; overflow-y: auto;
}
.section-label { font-size: 0.72rem; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); margin: 1rem 0 .25rem; }
.sidenav { list-style: none; margin: 0; padding: 0; }
.sidenav li { margin: .15rem 0; }
.sidenav a { color: var(--fg); text-decoration: none; font-size: .92rem; display: block; padding: .15rem .4rem; border-radius: 4px; }
.sidenav a:hover { background: var(--code-bg); }
.sidenav a.active { color: var(--accent); font-weight: 600; }
.content { flex: 1; min-width: 0; padding: 1.5rem 2.5rem 3rem; max-width: 880px; }
.toc { border: 1px solid var(--border); border-radius: 6px; padding: .6rem 1rem; margin-bottom: 1.5rem; font-size: .88rem; }
.toc strong { display: block; margin-bottom: .25rem; }
.toc ul { margin: .25rem 0; padding-left: 1.1rem; }
.toc li { margin: .1rem 0; }
.markdown h1 { font-size: 1.9rem; border-bottom: 1px solid var(--border); padding-bottom: .3rem; }
.markdown h2 { font-size: 1.4rem; margin-top: 1.6rem; border-bottom: 1px solid var(--border); padding-bottom: .25rem; }
.markdown h3 { font-size: 1.15rem; margin-top: 1.2rem; }
.markdown a { color: var(--accent); text-decoration: none; }
.markdown a:hover { text-decoration: underline; }
.markdown pre { background: var(--code-bg); padding: .9rem 1rem; border-radius: 6px; overflow-x: auto; font-size: .85rem; line-height: 1.5; }
.markdown code { background: var(--code-bg); padding: .15em .35em; border-radius: 4px; font-size: .88em; }
.markdown pre code { background: none; padding: 0; }
.markdown table { border-collapse: collapse; margin: 1rem 0; width: 100%; }
.markdown th, .markdown td { border: 1px solid var(--border); padding: .4rem .6rem; text-align: left; }
.markdown th { background: var(--code-bg); }
.markdown blockquote { border-left: 4px solid var(--border); margin: 1rem 0; padding: .1rem 1rem; color: var(--muted); }
.markdown img { max-width: 100%; }
.markdown hr { border: none; border-top: 1px solid var(--border); margin: 2rem 0; }
.site-footer { border-top: 1px solid var(--border); color: var(--muted); font-size: .8rem; text-align: center; padding: 1rem; margin-top: 2rem; }
@media (max-width: 720px) {
  .layout { flex-direction: column; }
  .sidebar { width: 100%; min-width: 0; height: auto; position: static; border-right: none; border-bottom: 1px solid var(--border); padding: 1rem; }
}
`
