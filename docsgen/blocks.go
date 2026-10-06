package docsgen

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Врезки пишутся синтаксисом GitHub alerts, чтобы Markdown оставался
// читаемым и на GitHub:
//
//	> [!NOTE]
//	> Текст врезки.
//
// Пять стандартных видов GitHub рендерит сам. Остальные (LEARN, PITFALL,
// DEEPDIVE) на GitHub выглядят как обычная цитата с меткой, а на сайте
// становятся оформленными блоками. После метки на той же строке можно указать
// свой заголовок: `> [!DEEPDIVE] Почему SHA-256, а не номер версии`.
type calloutKind struct {
	class       string
	title       string
	collapsible bool
}

var calloutKinds = map[string]calloutKind{
	"NOTE":      {class: "note", title: "Примечание"},
	"TIP":       {class: "tip", title: "Совет"},
	"IMPORTANT": {class: "important", title: "Важно"},
	"WARNING":   {class: "warning", title: "Внимание"},
	"CAUTION":   {class: "caution", title: "Осторожно"},
	"LEARN":     {class: "learn", title: "Вы узнаете"},
	"PITFALL":   {class: "pitfall", title: "Подводный камень"},
	"DEEPDIVE":  {class: "deepdive", title: "Подробнее", collapsible: true},
}

var calloutMarker = regexp.MustCompile(`^\[!([A-Z]+)\][ \t]*(.*)$`)

// KindCallout is the AST node kind for a rendered callout block.
var KindCallout = ast.NewNodeKind("Callout")

type calloutNode struct {
	ast.BaseBlock
	kind  calloutKind
	title string
}

func (n *calloutNode) Kind() ast.NodeKind { return KindCallout }

func (n *calloutNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Class": n.kind.class, "Title": n.title}, nil)
}

// KindFigure is the AST node kind for an image rendered with a caption.
var KindFigure = ast.NewNodeKind("Figure")

type figureNode struct {
	ast.BaseBlock
	caption string
}

func (n *figureNode) Kind() ast.NodeKind { return KindFigure }

func (n *figureNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Caption": n.caption}, nil)
}

// blockTransformer turns marked blockquotes into callouts and stand-alone
// images with a title (`![alt](shot.png "Подпись")`) into figures.
type blockTransformer struct{}

func (blockTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	var quotes []*ast.Blockquote
	var figures []*ast.Paragraph
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Blockquote:
			quotes = append(quotes, n)
		case *ast.Paragraph:
			if img, ok := n.FirstChild().(*ast.Image); ok && n.ChildCount() == 1 && len(img.Title) > 0 {
				figures = append(figures, n)
			}
		}
		return ast.WalkContinue, nil
	})
	for _, q := range quotes {
		convertCallout(q, source)
	}
	for _, p := range figures {
		img := p.FirstChild().(*ast.Image)
		fig := &figureNode{caption: string(img.Title)}
		img.Title = nil
		p.RemoveChild(p, img)
		fig.AppendChild(fig, img)
		p.Parent().ReplaceChild(p.Parent(), p, fig)
	}
}

func convertCallout(q *ast.Blockquote, source []byte) {
	para, ok := q.FirstChild().(*ast.Paragraph)
	if !ok || para.Lines().Len() == 0 {
		return
	}
	first := para.Lines().At(0)
	line := strings.TrimRight(string(first.Value(source)), "\r\n")
	m := calloutMarker.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return
	}
	kind, ok := calloutKinds[m[1]]
	if !ok {
		return
	}
	title := strings.TrimSpace(m[2])
	if title == "" {
		title = kind.title
	}

	// Убираем из абзаца inline-узлы первой строки (метку и заголовок).
	for c := para.FirstChild(); c != nil; {
		next := c.NextSibling()
		if inlineStart(c) >= first.Stop {
			break
		}
		para.RemoveChild(para, c)
		c = next
	}
	// Перенос строки после метки теперь в начале абзаца, а сам абзац может
	// оказаться пустым.
	if t, ok := para.FirstChild().(*ast.Text); ok && strings.TrimSpace(string(t.Segment.Value(source))) == "" {
		para.RemoveChild(para, t)
	}
	if para.ChildCount() == 0 {
		q.RemoveChild(q, para)
	}

	node := &calloutNode{kind: kind, title: title}
	for c := q.FirstChild(); c != nil; {
		next := c.NextSibling()
		q.RemoveChild(q, c)
		node.AppendChild(node, c)
		c = next
	}
	q.Parent().ReplaceChild(q.Parent(), q, node)
}

// inlineStart returns the source offset where an inline node begins, or -1
// when it has no text segment (it is then treated as part of the first line).
func inlineStart(n ast.Node) int {
	if t, ok := n.(*ast.Text); ok {
		return t.Segment.Start
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if s := inlineStart(c); s >= 0 {
			return s
		}
	}
	return -1
}

type blockRenderer struct{}

func (blockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindCallout, renderCallout)
	reg.Register(KindFigure, renderFigure)
}

func renderCallout(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	c := n.(*calloutNode)
	title := template.HTMLEscapeString(c.title)
	switch {
	case entering && c.kind.collapsible:
		_, _ = w.WriteString(`<details class="callout callout-` + c.kind.class + `"><summary>` + title + "</summary>\n")
	case entering:
		_, _ = w.WriteString(`<aside class="callout callout-` + c.kind.class + `"><p class="callout-title">` + title + "</p>\n")
	case c.kind.collapsible:
		_, _ = w.WriteString("</details>\n")
	default:
		_, _ = w.WriteString("</aside>\n")
	}
	return ast.WalkContinue, nil
}

func renderFigure(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	f := n.(*figureNode)
	if entering {
		_, _ = w.WriteString("<figure>")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("<figcaption>" + template.HTMLEscapeString(f.caption) + "</figcaption></figure>\n")
	return ast.WalkContinue, nil
}

// pageHeading is one h2/h3 heading of a rendered page, used for the in-page
// TOC and the search index.
type pageHeading struct {
	Level int
	ID    string
	Text  string
}

// collectHeadings walks a parsed document after the heading-ID transformer
// ran and returns h2/h3 headings in document order. Headings inside code
// blocks never reach the AST, unlike a line scan of the Markdown source.
func collectHeadings(doc ast.Node, source []byte) []pageHeading {
	var out []pageHeading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		id, ok := h.AttributeString("id")
		if !ok {
			return ast.WalkContinue, nil
		}
		idStr := string(id.([]byte))
		out = append(out, pageHeading{Level: h.Level, ID: idStr, Text: plainText(h, source)})
		return ast.WalkSkipChildren, nil
	})
	return out
}

func plainText(n ast.Node, source []byte) string {
	var b bytes.Buffer
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(source))
			if c.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			for g := c.FirstChild(); g != nil; g = g.NextSibling() {
				if t, ok := g.(*ast.Text); ok {
					b.Write(t.Segment.Value(source))
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}
