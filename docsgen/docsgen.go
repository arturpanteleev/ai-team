// Package docsgen builds a static documentation site from the repository's
// Markdown sources (README, docs/, CONTRIBUTING, SECURITY, CHANGELOG, ...).
//
// The Markdown files remain the authoritative, agent-friendly source of
// truth; docsgen turns them into a self-contained, browseable HTML site for
// humans. It is deterministic: identical Markdown in produces byte-identical
// output, which keeps the build reproducible and testable.
package docsgen

import (
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
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
	// Description is a one-line summary used for the meta description and
	// for page cards on the home page.
	Description string
}

// Page is a single documentation page derived from one Markdown file.
type Page struct {
	Source      string
	Title       string
	Section     string
	Weight      int
	URL         string
	Description string

	Body template.HTML
	TOC  template.HTML

	// Prev and Next link pages in reading order (sidebar order).
	Prev *Page
	Next *Page

	headings []pageHeading
}

// NavSection is one sidebar group with its pages in reading order.
type NavSection struct {
	Title string
	Pages []*Page
}

// NavLink is a labelled link in the header or on the home page. Root-relative
// URLs ("/start/") get the site base path; absolute URLs are kept as is.
type NavLink struct {
	Title string
	URL   string
}

// Site holds the full set of pages and shared metadata used by the layout.
type Site struct {
	Title       string
	Version     string
	BasePath    string
	Pages       []*Page
	Sections    []NavSection
	HeaderLinks []NavLink
	HeroActions []NavLink
	Current     *Page
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
	// SectionOrder lists sidebar section titles in display order. Sections
	// not listed go last; nil keeps the default English order (Guide,
	// Reference, Community, Project).
	SectionOrder []string
	// GitHubRepo is the "owner/repo" used to rewrite directory links to
	// GitHub blob URLs (e.g. "arturpanteleev/ai-team").
	GitHubRepo string
	// HeaderLinks are shown in the site header; HeroActions are the buttons
	// on the home page.
	HeaderLinks []NavLink
	HeroActions []NavLink
	// Aliases map repository Markdown files that are not rendered pages to a
	// site URL, so links to them lead somewhere useful (README.md → "/").
	Aliases map[string]string
}

// Build renders all configured Markdown sources into HTML under Output.
func Build(cfg Config) error {
	if cfg.CleanOutput && cfg.Output != "" {
		if err := os.RemoveAll(cfg.Output); err != nil {
			return fmt.Errorf("clean output: %w", err)
		}
	}

	site := &Site{
		Title:       cfg.Title,
		Version:     cfg.Version,
		BasePath:    normalizeBasePath(cfg.BasePath),
		HeaderLinks: cfg.HeaderLinks,
		HeroActions: cfg.HeroActions,
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

	// repoRoot is the actual repository root, used as the base for GitHub
	// blob/tree URLs. It is distinct from a linkTransformer's baseDir, which
	// is the directory of the specific page being rendered (see below) and
	// varies per page depth.
	for source, url := range cfg.Aliases {
		abs, err := filepath.Abs(filepath.Join(cfg.Root, filepath.FromSlash(source)))
		if err != nil {
			return err
		}
		if _, isPage := pathMap[abs]; !isPage {
			pathMap[abs] = joinBase(site.BasePath, url)
		}
	}

	repoRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
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
			repoRoot:   repoRoot,
			pathMap:    pathMap,
			pageURL:    url,
			githubRepo: cfg.GitHubRepo,
			sourcePage: sp.Source,
		}
		page, err := renderPage(tr, sp, content)
		if err != nil {
			return fmt.Errorf("render %s: %w", sp.Source, err)
		}
		nonPage = append(nonPage, tr.nonPageLinks...)
		site.Pages = append(site.Pages, page)
	}

	sortPages(site.Pages, cfg.SectionOrder)
	site.Sections = navSections(site.Pages)
	linkReadingOrder(site.Sections)

	if err := writeLayoutAssets(cfg.Output, site); err != nil {
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
	// Walker ниже возвращает только nil-ошибки, а ast.Walk других источников
	// ошибок не имеет — проверять нечего. Transform реализует интерфейс
	// goldmark parser.ASTTransformer и вернуть ошибку наверх не может.
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		// Heading.Text устарел в goldmark, и замены с идентичным поведением в
		// общем случае нет: Lines()/Text.Value расходятся с Text() на
		// заголовках со ссылками и картинками. На этом репозитории расхождения
		// сейчас нет — прогон обоих вариантов по всем .md дал 2744 заголовка и
		// 0 изменившихся якорей (inline-код вроде `make verify` даёт одинаковый
		// slug обоими способами). Так что nolint здесь — не «иначе сломаются
		// якоря», а дисциплина скоупа: миграция публичного API доков не входит
		// в PR про подключение линтеров. Снимать этот nolint нужно вместе с
		// переходом на новый API и проверкой якорей, а не молча.
		base := slugifyID(string(h.Text(source))) //nolint:staticcheck // SA1019: миграция API доков вне скоупа этого PR
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
	baseDir      string // directory of the page currently being rendered (varies per page)
	repoRoot     string // actual repository root, used for GitHub blob/tree URLs
	pathMap      map[string]string
	pageURL      string // site URL of the page being rendered (e.g. "/contributing/")
	githubRepo   string
	sourcePage   string // repo-relative source path of the page being rendered, for diagnostics
	nonPageLinks []nonPageLink
}

// nonPageLink tracks a relative link to a repository file that is not among
// the rendered pages and needs to be copied into the site output.
type nonPageLink struct {
	resolved   string // absolute repo path of the target file
	outPath    string // relative path within the output directory
	sourcePage string // repo-relative source path of the page that linked to it
}

func (t *linkTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	// Как и в headingIDTransformer: walker не возвращает ошибок, а сигнатура
	// ASTTransformer не позволяет их пробросить.
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
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
		resolved:   resolved,
		outPath:    filepath.Join(filepath.FromSlash(strings.Trim(strings.TrimPrefix(t.pageURL, "/"), "/")), relDir, filepath.Base(resolved)),
		sourcePage: t.sourcePage,
	}
}

// slashRelRepoTarget computes the repo-root-relative path of a resolved file,
// used to build GitHub blob/tree URLs. This is always relative to the actual
// repository root (t.repoRoot), never to the rendering page's own directory
// (t.baseDir) — the two coincide only for pages that live at the repo root,
// so using baseDir here would produce a wrong GitHub URL for any page nested
// in a subdirectory (e.g. docs/demo/README.md).
func slashRelRepoTarget(t *linkTransformer, resolved string) string {
	rel, err := filepath.Rel(t.repoRoot, resolved)
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
//
// A page nested deep enough in the source tree, linking far enough upward
// (e.g. "../../LICENSE"), can compute an output path that normalizes outside
// output entirely. This should never happen for a well-formed docs tree, so
// every destination is verified to be contained within output before
// anything is created; a violation is a hard build failure (naming the
// offending source page and target), not a silent skip.
func copyAssets(links []nonPageLink, output string) error {
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}
	for _, l := range links {
		outPath, err := filepath.Abs(filepath.Join(output, filepath.FromSlash(l.outPath)))
		if err != nil {
			return fmt.Errorf("resolve asset path %s: %w", l.outPath, err)
		}
		if outPath != outputAbs && !strings.HasPrefix(outPath, outputAbs+string(filepath.Separator)) {
			return fmt.Errorf("refusing to write asset outside output directory: page %s links to %s, which resolves to %s (outside %s)",
				l.sourcePage, l.resolved, outPath, outputAbs)
		}
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
