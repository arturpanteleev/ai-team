// Package gatephrases implements the logic of the required_phrases code gate:
// a deterministic check that a Markdown result contains required words or
// fragments. Every dimension of the check is stated by the template author and
// has no hidden default that could turn a typo into a pass: the letter case,
// the search scope (the whole result or one section by heading) and the match
// rule (all, any, at least N; substring or whole word).
//
// The package runs no user-supplied code. Regular expressions are an explicit
// opt-in (regex: true) and are Go RE2 only: linear time, no backtracking, with
// bounded pattern length and count, all compiled when the configuration is
// validated, so an invalid pattern is a configuration error and never a pass.
package gatephrases

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parameter names of the gate definition.
const (
	ParamPhrases  = "phrases"
	ParamCase     = "case"
	ParamScope    = "scope"
	ParamSection  = "section"
	ParamMatch    = "match"
	ParamMinCount = "min_count"
	ParamUnit     = "unit"
	ParamRegex    = "regex"
)

// Enumerated parameter values.
const (
	CaseSensitive   = "sensitive"
	CaseInsensitive = "insensitive"

	ScopeResult  = "result"
	ScopeSection = "section"

	MatchAll     = "all"
	MatchAny     = "any"
	MatchAtLeast = "at_least"

	UnitSubstring = "substring"
	UnitWord      = "word"
)

// Limits keep the gate cheap and its configuration reviewable.
const (
	MaxPhrases     = 50
	MaxPhraseBytes = 200
)

// Allowed values per enum parameter; gatelib builds the definition from them
// so the catalog and the validation cannot drift apart.
var (
	CaseValues  = []string{CaseSensitive, CaseInsensitive}
	ScopeValues = []string{ScopeResult, ScopeSection}
	MatchValues = []string{MatchAll, MatchAny, MatchAtLeast}
	UnitValues  = []string{UnitSubstring, UnitWord}
)

// Spec is a validated gate configuration.
type Spec struct {
	Phrases       []string
	CaseSensitive bool
	Scope         string
	Section       string
	Match         string
	MinCount      int
	Unit          string
	Regex         bool

	patterns []*regexp.Regexp
}

// Parse validates the parameters and returns the Spec. Every error names the
// parameter and never repeats a phrase or a pattern value.
func Parse(params map[string]any) (*Spec, error) {
	spec := &Spec{}
	for name := range params {
		switch name {
		case ParamPhrases, ParamCase, ParamScope, ParamSection, ParamMatch, ParamMinCount, ParamUnit, ParamRegex:
		default:
			return nil, fmt.Errorf("неизвестный параметр %q", name)
		}
	}

	phrases, ok := stringList(params[ParamPhrases])
	if !ok || len(phrases) == 0 {
		return nil, fmt.Errorf("параметр %q обязателен и должен быть непустым списком строк", ParamPhrases)
	}
	if len(phrases) > MaxPhrases {
		return nil, fmt.Errorf("параметр %q: не больше %d фраз", ParamPhrases, MaxPhrases)
	}
	for i, phrase := range phrases {
		if strings.TrimSpace(phrase) == "" || strings.TrimSpace(phrase) != phrase {
			return nil, fmt.Errorf("параметр %q[%d]: фраза пуста или с пробелами по краям", ParamPhrases, i)
		}
		if len(phrase) > MaxPhraseBytes {
			return nil, fmt.Errorf("параметр %q[%d]: длиннее %d байт", ParamPhrases, i, MaxPhraseBytes)
		}
		if !utf8.ValidString(phrase) {
			return nil, fmt.Errorf("параметр %q[%d]: не валидный UTF-8", ParamPhrases, i)
		}
		// A control character (CR, NUL, ESC, ...) is a copy-paste accident that
		// can never match the line-normalized text. Newline and tab are kept.
		if strings.IndexFunc(phrase, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
			return nil, fmt.Errorf("параметр %q[%d]: содержит управляющий символ", ParamPhrases, i)
		}
	}
	spec.Phrases = phrases

	caseValue, err := enumValue(params, ParamCase, CaseValues)
	if err != nil {
		return nil, err
	}
	spec.CaseSensitive = caseValue == CaseSensitive
	if spec.Scope, err = enumValue(params, ParamScope, ScopeValues); err != nil {
		return nil, err
	}
	if spec.Match, err = enumValue(params, ParamMatch, MatchValues); err != nil {
		return nil, err
	}

	if raw, set := params[ParamRegex]; set {
		flag, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("параметр %q должен быть true или false", ParamRegex)
		}
		spec.Regex = flag
	}
	spec.Unit = UnitSubstring
	if _, set := params[ParamUnit]; set {
		if spec.Unit, err = enumValue(params, ParamUnit, UnitValues); err != nil {
			return nil, err
		}
	}
	if spec.Regex && spec.Unit == UnitWord {
		return nil, fmt.Errorf("параметр %q: unit %s несовместим с regex: true (границы слова задаются в самом выражении)", ParamUnit, UnitWord)
	}

	section, sectionSet := params[ParamSection]
	if spec.Scope == ScopeSection {
		text, ok := section.(string)
		if !sectionSet || !ok || strings.TrimSpace(text) == "" || strings.TrimSpace(text) != text {
			return nil, fmt.Errorf("параметр %q обязателен при scope: %s и должен быть непустой строкой без пробелов по краям", ParamSection, ScopeSection)
		}
		if normalizeHeading(strings.TrimLeft(text, "#")) == "" {
			return nil, fmt.Errorf("параметр %q не задаёт название заголовка", ParamSection)
		}
		spec.Section = text
	} else if sectionSet {
		return nil, fmt.Errorf("параметр %q допустим только при scope: %s", ParamSection, ScopeSection)
	}

	minRaw, minSet := params[ParamMinCount]
	if spec.Match == MatchAtLeast {
		count, ok := intValue(minRaw)
		if !minSet || !ok {
			return nil, fmt.Errorf("параметр %q обязателен при match: %s и должен быть целым числом", ParamMinCount, MatchAtLeast)
		}
		if count < 1 || count > len(phrases) {
			return nil, fmt.Errorf("параметр %q должен быть от 1 до числа фраз (%d)", ParamMinCount, len(phrases))
		}
		spec.MinCount = count
	} else if minSet {
		return nil, fmt.Errorf("параметр %q допустим только при match: %s", ParamMinCount, MatchAtLeast)
	}

	seen := map[string]bool{}
	for i, phrase := range phrases {
		key := phrase
		if !spec.CaseSensitive {
			key = foldCase(phrase)
		}
		if seen[key] {
			return nil, fmt.Errorf("параметр %q[%d]: фраза повторяется (с учётом выбранного регистра)", ParamPhrases, i)
		}
		seen[key] = true
	}

	// Phrases are counted by distinct entry, not by position in the text. When
	// one literal phrase contains another, a single fragment of the text would
	// satisfy both and at_least would pass on fewer distinct fragments than
	// min_count says. That is ambiguous, so it is a configuration error for
	// at_least; all and any are not affected by overlap. Regular expressions
	// cannot be compared and are not checked.
	if spec.Match == MatchAtLeast && !spec.Regex {
		for i := range phrases {
			for j := range phrases {
				if i == j {
					continue
				}
				outer, inner := phrases[i], phrases[j]
				if !spec.CaseSensitive {
					outer, inner = foldCase(outer), foldCase(inner)
				}
				if strings.Contains(outer, inner) {
					return nil, fmt.Errorf("параметр %q[%d]: содержится во фразе [%d]; при match: %s пересекающиеся фразы считались бы одним фрагментом", ParamPhrases, j, i, MatchAtLeast)
				}
			}
		}
	}

	if spec.Regex {
		for i, phrase := range phrases {
			expression := phrase
			if !spec.CaseSensitive {
				expression = "(?i)" + expression
			}
			compiled, err := regexp.Compile(expression)
			if err != nil {
				return nil, fmt.Errorf("параметр %q[%d]: выражение не компилируется как RE2", ParamPhrases, i)
			}
			if matchesEmpty(compiled) {
				return nil, fmt.Errorf("параметр %q[%d]: выражение допускает совпадение нулевой длины и прошло бы любой результат", ParamPhrases, i)
			}
			spec.patterns = append(spec.patterns, compiled)
		}
	}
	return spec, nil
}

// emptyProbes are the strings a pattern is tried on to find zero-length
// matches: empty, blank, a word, several words.
var emptyProbes = []string{"", " ", "\n", "word", "two words", " a b ", "слово два", "x,y."}

// matchesEmpty reports whether the pattern can match zero-length text anywhere
// in the probes (covers a*, \b, ^, $, a|\b). Execution additionally ignores
// zero-length matches, so a miss here still cannot turn into a pass.
func matchesEmpty(re *regexp.Regexp) bool {
	for _, probe := range emptyProbes {
		for _, at := range re.FindAllStringIndex(probe, -1) {
			if at[0] == at[1] {
				return true
			}
		}
	}
	return false
}

// Validate is Parse without the result.
func Validate(params map[string]any) error {
	_, err := Parse(params)
	return err
}

// Result is the outcome of one check.
type Result struct {
	// Satisfied is the verdict: true is pass, false is fail.
	Satisfied bool
	// Matched and Missing list the phrases in the order of Spec.Phrases.
	Matched []string
	Missing []string
	Reason  string
}

// Check evaluates the spec over a Markdown result. An empty result and a
// missing section fail; a pass needs the phrases the rule requires.
//
// The text is compared as it is, except for line endings (CRLF and a lone CR
// become LF) and a leading byte order mark, which are dropped. Unicode
// normalization is not applied: a precomposed letter and its decomposed form
// (base letter plus combining mark) are different texts, as are a no-break
// space and a space.
func (s *Spec) Check(content string) Result {
	content = normalizeContent(content)
	if strings.TrimSpace(content) == "" {
		return Result{Reason: "результат пуст", Missing: append([]string(nil), s.Phrases...)}
	}
	scope := content
	if s.Scope == ScopeSection {
		body, found := sectionBody(content, s.Section)
		if !found {
			return Result{Reason: fmt.Sprintf("раздел %q не найден", s.Section), Missing: append([]string(nil), s.Phrases...)}
		}
		scope = body
	}
	haystack := scope
	if !s.CaseSensitive && !s.Regex {
		haystack = foldCase(scope)
	}
	var result Result
	for i, phrase := range s.Phrases {
		if s.present(haystack, i, phrase) {
			result.Matched = append(result.Matched, phrase)
		} else {
			result.Missing = append(result.Missing, phrase)
		}
	}
	switch s.Match {
	case MatchAll:
		result.Satisfied = len(result.Missing) == 0
	case MatchAny:
		result.Satisfied = len(result.Matched) >= 1
	case MatchAtLeast:
		result.Satisfied = len(result.Matched) >= s.MinCount
	}
	if !result.Satisfied {
		where := "в результате"
		if s.Scope == ScopeSection {
			where = fmt.Sprintf("в разделе %q", s.Section)
		}
		switch s.Match {
		case MatchAll:
			result.Reason = fmt.Sprintf("%s не найдены обязательные фразы: %s", where, quoteList(result.Missing))
		case MatchAny:
			result.Reason = fmt.Sprintf("%s не найдена ни одна из фраз: %s", where, quoteList(result.Missing))
		default:
			result.Reason = fmt.Sprintf("%s найдено фраз %d, требуется не меньше %d (не найдены: %s)", where, len(result.Matched), s.MinCount, quoteList(result.Missing))
		}
	}
	return result
}

// normalizeContent unifies line endings and drops a leading byte order mark.
func normalizeContent(content string) string {
	content = strings.TrimPrefix(content, "\ufeff")
	if strings.Contains(content, "\r") {
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
	}
	return content
}

func (s *Spec) present(haystack string, index int, phrase string) bool {
	if s.Regex {
		for _, at := range s.patterns[index].FindAllStringIndex(haystack, -1) {
			if at[1] > at[0] {
				return true
			}
		}
		return false
	}
	needle := phrase
	if !s.CaseSensitive {
		needle = foldCase(phrase)
	}
	if s.Unit == UnitSubstring {
		return strings.Contains(haystack, needle)
	}
	return containsWord(haystack, needle)
}

// containsWord finds needle delimited on both sides by a non-word rune or a
// text edge. A side is checked only if the needle itself starts (ends) with a
// word rune: for "C++" only the left side is a word boundary.
func containsWord(haystack, needle string) bool {
	first, _ := utf8.DecodeRuneInString(needle)
	last, _ := utf8.DecodeLastRuneInString(needle)
	for start := 0; start <= len(haystack)-len(needle); {
		at := strings.Index(haystack[start:], needle)
		if at < 0 {
			return false
		}
		from := start + at
		to := from + len(needle)
		ok := true
		if isWordRune(first) && from > 0 {
			before, _ := utf8.DecodeLastRuneInString(haystack[:from])
			ok = !isWordRune(before)
		}
		if ok && isWordRune(last) && to < len(haystack) {
			after, _ := utf8.DecodeRuneInString(haystack[to:])
			ok = !isWordRune(after)
		}
		if ok {
			return true
		}
		_, width := utf8.DecodeRuneInString(haystack[from:])
		start = from + width
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// sectionBody returns the text under every heading titled section (compared
// like required_sections: case-folded, trailing colons ignored), up to the next
// heading of the same or a higher level; subsections belong to the body.
//
// Heading syntax follows CommonMark: an ATX heading is 1-6 '#' followed by a
// space, a tab or the end of the line, indented by at most 3 spaces (an
// optional closing '#' sequence is dropped); a setext heading is a paragraph
// underlined with '=' (level 1) or '-' (level 2). Lines inside fenced code
// blocks, indented code (4+ spaces) and HTML comments are never headings. A
// closing fence is the same character, not shorter than the opening one, with
// no info string.
func sectionBody(content, section string) (string, bool) {
	target := normalizeHeading(strings.TrimLeft(section, "#"))
	var out strings.Builder
	found, capturing := false, false
	level := 0
	fence := ""
	inComment := false
	var paragraph []string

	// open applies a heading of the given depth and title; it reports whether
	// the heading line itself must be left out of the body.
	open := func(depth int, title string) bool {
		if capturing && depth <= level {
			capturing = false
		}
		if !capturing && normalizeHeading(title) == target {
			capturing, found, level = true, true, depth
			return true
		}
		return false
	}
	flush := func() {
		if capturing {
			for _, line := range paragraph {
				out.WriteString(line + "\n")
			}
		}
		paragraph = paragraph[:0]
	}

	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if inComment {
			flush()
			if capturing {
				out.WriteString(line + "\n")
			}
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if fence != "" {
			flush()
			if closesFence(line, fence) {
				fence = ""
			}
			if capturing {
				out.WriteString(line + "\n")
			}
			continue
		}
		if marker := openingFence(line); marker != "" {
			flush()
			fence = marker
			if capturing {
				out.WriteString(line + "\n")
			}
			continue
		}
		if depth, title, ok := atxHeading(line); ok {
			flush()
			if !open(depth, title) && capturing {
				out.WriteString(line + "\n")
			}
			continue
		}
		if len(paragraph) > 0 {
			if depth := setextUnderline(line); depth > 0 {
				title := strings.Join(paragraph, " ")
				paragraph = paragraph[:0]
				if !open(depth, title) && capturing {
					out.WriteString(title + "\n" + line + "\n")
				}
				continue
			}
		}
		if strings.TrimSpace(line) == "" {
			flush()
			if capturing {
				out.WriteString(line + "\n")
			}
			continue
		}
		if start := strings.Index(line, "<!--"); start >= 0 && !strings.Contains(line[start+4:], "-->") {
			inComment = true
		}
		if indentOf(line) >= 4 && len(paragraph) == 0 {
			// Indented code block: neither a heading nor a paragraph.
			if capturing {
				out.WriteString(line + "\n")
			}
			continue
		}
		paragraph = append(paragraph, line)
		if inComment {
			flush()
		}
	}
	flush()
	return out.String(), found
}

// indentOf counts the leading columns; a tab counts as reaching column 4.
func indentOf(line string) int {
	columns := 0
	for _, r := range line {
		switch r {
		case ' ':
			columns++
		case '\t':
			return columns + 4 - columns%4
		default:
			return columns
		}
	}
	return columns
}

// atxHeading parses "# Title" with up to 3 spaces of indent.
func atxHeading(line string) (int, string, bool) {
	if indentOf(line) >= 4 {
		return 0, "", false
	}
	rest := strings.TrimLeft(line, " ")
	depth := len(rest) - len(strings.TrimLeft(rest, "#"))
	if depth < 1 || depth > 6 {
		return 0, "", false
	}
	rest = rest[depth:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	title := strings.TrimSpace(rest)
	if trimmed := strings.TrimRight(title, "#"); trimmed != title {
		if trimmed == "" {
			title = ""
		} else if last := trimmed[len(trimmed)-1]; last == ' ' || last == '\t' {
			title = strings.TrimSpace(trimmed)
		}
	}
	return depth, title, true
}

// setextUnderline returns 1 for a "===" line, 2 for a "---" line, 0 otherwise.
func setextUnderline(line string) int {
	if indentOf(line) >= 4 {
		return 0
	}
	body := strings.TrimSpace(line)
	if body == "" {
		return 0
	}
	switch {
	case strings.Trim(body, "=") == "":
		return 1
	case strings.Trim(body, "-") == "":
		return 2
	}
	return 0
}

// openingFence returns the fence marker of an opening line, or "".
func openingFence(line string) string {
	if indentOf(line) >= 4 {
		return ""
	}
	rest := strings.TrimLeft(line, " ")
	for _, char := range []string{"`", "~"} {
		count := len(rest) - len(strings.TrimLeft(rest, char))
		if count < 3 {
			continue
		}
		if char == "`" && strings.Contains(rest[count:], "`") {
			return ""
		}
		return strings.Repeat(char, count)
	}
	return ""
}

// closesFence reports whether line closes a fence opened with marker.
func closesFence(line, marker string) bool {
	if indentOf(line) >= 4 {
		return false
	}
	rest := strings.TrimSpace(line)
	count := len(rest) - len(strings.TrimLeft(rest, marker[:1]))
	return count >= len(marker) && count == len(rest)
}

func normalizeHeading(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, ":")
	return foldCase(strings.TrimSpace(value))
}

// foldCase maps every rune to the smallest member of its simple case-folding
// orbit, so that sigma, final sigma and capital sigma (and ß and capital ẞ)
// compare equal. It works rune by rune and never changes the rune count, unlike
// strings.ToLower on special runes. Full folding (ß to ss) is not applied.
func foldCase(value string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, value)
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return strings.Join(quoted, ", ")
}

func enumValue(params map[string]any, name string, allowed []string) (string, error) {
	if value, ok := params[name].(string); ok {
		for _, candidate := range allowed {
			if candidate == value {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("параметр %q обязателен и должен быть одним из: %s", name, strings.Join(allowed, ", "))
}

func stringList(value any) ([]string, bool) {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...), true
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	}
	return nil, false
}

func intValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		if number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}
