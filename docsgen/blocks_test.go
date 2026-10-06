package docsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderMarkdown(t *testing.T, src string) string {
	t.Helper()
	tr := &linkTransformer{baseDir: t.TempDir(), pathMap: map[string]string{}, pageURL: "/"}
	page, err := renderPage(tr, SourcedPage{Source: "x.md", URL: "/x/"}, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return string(page.Body)
}

func TestCalloutsRenderAsBlocks(t *testing.T) {
	body := renderMarkdown(t, "> [!WARNING]\n> Агент работает с вашими правами.\n\n"+
		"> [!DEEPDIVE] Почему SHA-256\n> Хеш связывает решение с содержимым.\n\n"+
		"> [!LEARN]\n> - первое\n> - второе\n\n"+
		"> Обычная цитата.\n")

	for _, want := range []string{
		`<aside class="callout callout-warning"><p class="callout-title">Внимание</p>`,
		"<p>Агент работает с вашими правами.</p>",
		`<details class="callout callout-deepdive"><summary>Почему SHA-256</summary>`,
		"<p>Хеш связывает решение с содержимым.</p>",
		`<aside class="callout callout-learn"><p class="callout-title">Вы узнаете</p>`,
		"<li>первое</li>",
		"<blockquote>\n<p>Обычная цитата.</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[!") {
		t.Errorf("callout marker leaked into output:\n%s", body)
	}
}

func TestUnknownCalloutStaysQuote(t *testing.T) {
	body := renderMarkdown(t, "> [!SOMETHING]\n> текст\n")
	if !strings.Contains(body, "<blockquote>") || strings.Contains(body, "callout") {
		t.Errorf("unknown marker must stay a plain blockquote:\n%s", body)
	}
}

func TestImageWithTitleBecomesFigure(t *testing.T) {
	body := renderMarkdown(t, "![Дашборд](shot.png \"Список прогонов\")\n\n![Без подписи](plain.png)\n")
	if !strings.Contains(body, `<figure><img src="shot.png" alt="Дашборд"><figcaption>Список прогонов</figcaption></figure>`) {
		t.Errorf("titled image not rendered as figure:\n%s", body)
	}
	if !strings.Contains(body, `<p><img src="plain.png" alt="Без подписи"></p>`) {
		t.Errorf("untitled image must stay inline:\n%s", body)
	}
}

func TestTOCIgnoresHeadingsInCode(t *testing.T) {
	toc := buildTOC([]byte("# T\n\n## Настоящий\n\n```bash\n## не заголовок\n```\n"))
	if !strings.Contains(toc, "#настоящий") || strings.Contains(toc, "не заголовок") {
		t.Errorf("TOC must list only real headings:\n%s", toc)
	}
}

func TestNavigationGroupsSectionsAndLinksReadingOrder(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.md": "Главная\n",
		"a.md":     "# A\n",
		"b.md":     "# B\n",
		"c.md":     "# C\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	err := Build(Config{
		Root: root, Output: out, Title: "T",
		SectionOrder: []string{"Первый", "Второй"},
		Sources: []SourcedPage{
			{Source: "index.md", URL: "/"},
			{Source: "c.md", Section: "Второй", URL: "/c/"},
			{Source: "b.md", Section: "Первый", Weight: 1, URL: "/b/"},
			{Source: "a.md", Section: "Первый", Weight: 0, URL: "/a/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "b", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if strings.Index(page, ">Первый<") > strings.Index(page, ">Второй<") {
		t.Error("sidebar sections are not in SectionOrder")
	}
	for _, want := range []string{
		`<a class="pager-prev" href="/a/"><span>Назад</span>A</a>`,
		`<a class="pager-next" href="/c/"><span>Дальше</span>C</a>`,
		`href="/b/" class="active" aria-current="page"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("b/index.html missing %s", want)
		}
	}
	if err := CheckLinks(out); err != nil {
		t.Error(err)
	}
}
