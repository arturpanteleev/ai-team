package docsgen

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

//go:embed layout.html
var layoutSrc string

//go:embed site.css
var siteCSS []byte

//go:embed search.js
var searchJS []byte

func newMarkdown(transformers ...util.PrioritizedValue) goldmark.Markdown {
	transformers = append(transformers,
		util.Prioritized(&headingIDTransformer{}, 200),
		util.Prioritized(blockTransformer{}, 300),
	)
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Table, extension.Linkify),
		goldmark.WithParserOptions(parser.WithASTTransformers(transformers...)),
		goldmark.WithRendererOptions(
			gmhtml.WithUnsafe(),
			renderer.WithNodeRenderers(util.Prioritized(blockRenderer{}, 500)),
		),
	)
}

// renderPage converts Markdown bytes into a Page with a body and TOC.
// tr carries the transformation configuration (baseDir, pathMap, pageURL and
// githubRepo) and accumulates any non-page relative links it finds.
func renderPage(tr *linkTransformer, sp SourcedPage, content []byte) (*Page, error) {
	md := newMarkdown(util.Prioritized(tr, 100))
	doc := md.Parser().Parse(text.NewReader(content))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, content, doc); err != nil {
		return nil, err
	}
	headings := collectHeadings(doc, content)

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
		Source:      sp.Source,
		Title:       title,
		Section:     sp.Section,
		Weight:      sp.Weight,
		URL:         url,
		Description: sp.Description,
		Body:        template.HTML(buf.String()),
		TOC:         template.HTML(tocHTML(headings)),
		headings:    headings,
	}, nil
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

// buildTOC renders the in-page table of contents (h2/h3) for Markdown
// content. Anchors match the IDs the page renderer assigns.
func buildTOC(content []byte) string {
	md := newMarkdown()
	doc := md.Parser().Parse(text.NewReader(content))
	return tocHTML(collectHeadings(doc, content))
}

func tocHTML(headings []pageHeading) string {
	if len(headings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<nav class="toc" aria-label="На этой странице"><p class="toc-title">На этой странице</p><ul>`)
	open := false
	for i, h := range headings {
		if h.Level == 3 && !open && i > 0 {
			b.WriteString("<ul>")
			open = true
		} else if h.Level == 2 && open {
			b.WriteString("</ul>")
			open = false
		}
		fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`, h.ID, template.HTMLEscapeString(h.Text))
	}
	if open {
		b.WriteString("</ul>")
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

var defaultSectionOrder = []string{"Guide", "Reference", "Community", "Project"}

func sortPages(pages []*Page, order []string) {
	if order == nil {
		order = defaultSectionOrder
	}
	rank := func(s string) int {
		for i, o := range order {
			if o == s {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Section != pages[j].Section {
			return rank(pages[i].Section) < rank(pages[j].Section)
		}
		return pages[i].Weight < pages[j].Weight
	})
}

// navSections groups already sorted pages into sidebar sections. The site
// index is not listed: the brand link leads there.
func navSections(pages []*Page) []NavSection {
	var out []NavSection
	for _, p := range pages {
		if p.URL == "/" {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Title != p.Section {
			out = append(out, NavSection{Title: p.Section})
		}
		out[len(out)-1].Pages = append(out[len(out)-1].Pages, p)
	}
	return out
}

func linkReadingOrder(sections []NavSection) {
	var prev *Page
	for _, s := range sections {
		for _, p := range s.Pages {
			if prev != nil {
				prev.Next = p
				p.Prev = prev
			}
			prev = p
		}
	}
}

func executeLayout(site *Site) ([]byte, error) {
	funcs := template.FuncMap{
		"link": func(url string) string {
			if strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "//") {
				return joinBase(site.BasePath, url)
			}
			return url
		},
	}
	layout, err := template.New("layout").Funcs(funcs).Parse(layoutSrc)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := layout.Execute(&buf, site); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeLayoutAssets(output string, site *Site) error {
	dir := filepath.Join(output, "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	index, err := searchIndex(site)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"site.css":        siteCSS,
		"search.js":       searchJS,
		"search-index.js": index,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

type searchEntry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Page  string `json:"page"`
	Text  string `json:"text"`
}

var (
	tagRe        = regexp.MustCompile(`<[^>]*>`)
	spaceRe      = regexp.MustCompile(`\s+`)
	headingTagRe = regexp.MustCompile(`<h[23][^>]*\sid="([^"]*)"[^>]*>`)
)

// searchIndex splits every rendered page into sections at h2/h3 headings and
// emits a deterministic JavaScript index (window.aiTeamSearch). Fragments use
// the same IDs as the rendered headings, so results land on the section.
func searchIndex(site *Site) ([]byte, error) {
	var entries []searchEntry
	for _, p := range site.Pages {
		body := string(p.Body)
		locs := headingTagRe.FindAllStringSubmatchIndex(body, -1)
		titles := make(map[string]string, len(p.headings))
		for _, h := range p.headings {
			titles[h.ID] = h.Text
		}
		pageURL := joinBase(site.BasePath, p.URL)
		end := len(body)
		if len(locs) > 0 {
			end = locs[0][0]
		}
		entries = append(entries, searchEntry{URL: pageURL, Title: p.Title, Page: p.Section, Text: plainHTML(body[:end])})
		for i, loc := range locs {
			id := body[loc[2]:loc[3]]
			stop := len(body)
			if i+1 < len(locs) {
				stop = locs[i+1][0]
			}
			title := titles[id]
			if title == "" {
				continue
			}
			entries = append(entries, searchEntry{
				URL:   pageURL + "#" + id,
				Title: title,
				Page:  p.Title,
				Text:  plainHTML(body[loc[1]:stop]),
			})
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	return append(append([]byte("window.aiTeamSearch="), data...), ";\n"...), nil
}

func plainHTML(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	const max = 1200
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}
