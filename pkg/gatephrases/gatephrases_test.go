package gatephrases

import (
	"strings"
	"testing"
)

func params(overrides map[string]any) map[string]any {
	base := map[string]any{
		ParamPhrases: []any{"alpha", "beta"},
		ParamCase:    CaseInsensitive,
		ParamScope:   ScopeResult,
		ParamMatch:   MatchAll,
	}
	for key, value := range overrides {
		if value == nil {
			delete(base, key)
			continue
		}
		base[key] = value
	}
	return base
}

func TestParseRejectsInvalidParameters(t *testing.T) {
	long := strings.Repeat("x", MaxPhraseBytes+1)
	tooMany := make([]any, MaxPhrases+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("p", 3) + string(rune('a'+i%26)) + strings.Repeat("q", i)
	}
	cases := map[string]map[string]any{
		"no phrases":            params(map[string]any{ParamPhrases: nil}),
		"empty phrases":         params(map[string]any{ParamPhrases: []any{}}),
		"blank phrase":          params(map[string]any{ParamPhrases: []any{"  "}}),
		"padded phrase":         params(map[string]any{ParamPhrases: []any{" a"}}),
		"non string phrase":     params(map[string]any{ParamPhrases: []any{1}}),
		"long phrase":           params(map[string]any{ParamPhrases: []any{long}}),
		"too many phrases":      params(map[string]any{ParamPhrases: tooMany}),
		"duplicate by case":     params(map[string]any{ParamPhrases: []any{"A", "a"}}),
		"case missing":          params(map[string]any{ParamCase: nil}),
		"case unknown":          params(map[string]any{ParamCase: "upper"}),
		"scope missing":         params(map[string]any{ParamScope: nil}),
		"scope unknown":         params(map[string]any{ParamScope: "paragraph"}),
		"match missing":         params(map[string]any{ParamMatch: nil}),
		"match unknown":         params(map[string]any{ParamMatch: "most"}),
		"section without scope": params(map[string]any{ParamSection: "Итог"}),
		"scope section empty":   params(map[string]any{ParamScope: ScopeSection}),
		"scope section blank":   params(map[string]any{ParamScope: ScopeSection, ParamSection: "###"}),
		"at_least no min":       params(map[string]any{ParamMatch: MatchAtLeast}),
		"at_least zero":         params(map[string]any{ParamMatch: MatchAtLeast, ParamMinCount: 0}),
		"at_least too big":      params(map[string]any{ParamMatch: MatchAtLeast, ParamMinCount: 3}),
		"at_least not int":      params(map[string]any{ParamMatch: MatchAtLeast, ParamMinCount: "1"}),
		"min without at_least":  params(map[string]any{ParamMinCount: 1}),
		"unit unknown":          params(map[string]any{ParamUnit: "line"}),
		"regex not bool":        params(map[string]any{ParamRegex: "yes"}),
		"regex bad pattern":     params(map[string]any{ParamRegex: true, ParamPhrases: []any{"(unclosed"}}),
		"regex lookahead":       params(map[string]any{ParamRegex: true, ParamPhrases: []any{"a(?=b)"}}),
		"regex matches empty":   params(map[string]any{ParamRegex: true, ParamPhrases: []any{"a*"}}),
		"regex with word":       params(map[string]any{ParamRegex: true, ParamUnit: UnitWord}),
		"unknown parameter":     params(map[string]any{"script": "x"}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(input); err == nil {
				t.Fatal("an invalid parameter must be a configuration error")
			}
		})
	}
}

func TestParseErrorsDoNotLeakPhraseValues(t *testing.T) {
	_, err := Parse(params(map[string]any{ParamRegex: true, ParamPhrases: []any{"s3cr3t-(unclosed"}}))
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error must name the parameter, not its value: %v", err)
	}
}

func mustParse(t *testing.T, overrides map[string]any) *Spec {
	t.Helper()
	spec, err := Parse(params(overrides))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestCheckMatchRules(t *testing.T) {
	content := "# Doc\n\nAlpha is here.\n"
	cases := []struct {
		name      string
		overrides map[string]any
		want      bool
	}{
		{"all missing one", nil, false},
		{"all present", map[string]any{ParamPhrases: []any{"alpha"}}, true},
		{"any one", map[string]any{ParamMatch: MatchAny}, true},
		{"any none", map[string]any{ParamMatch: MatchAny, ParamPhrases: []any{"gamma", "delta"}}, false},
		{"at least met", map[string]any{ParamMatch: MatchAtLeast, ParamMinCount: 1}, true},
		{"at least unmet", map[string]any{ParamMatch: MatchAtLeast, ParamMinCount: 2}, false},
		{"case sensitive miss", map[string]any{ParamCase: CaseSensitive, ParamPhrases: []any{"alpha"}}, false},
		{"case sensitive hit", map[string]any{ParamCase: CaseSensitive, ParamPhrases: []any{"Alpha"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mustParse(t, tc.overrides).Check(content)
			if result.Satisfied != tc.want {
				t.Fatalf("satisfied = %v, want %v (%s)", result.Satisfied, tc.want, result.Reason)
			}
			if !result.Satisfied && result.Reason == "" {
				t.Fatal("a failure must carry a reason")
			}
		})
	}
}

func TestCheckWholeWordVersusSubstring(t *testing.T) {
	spec := func(unit string) *Spec {
		return mustParse(t, map[string]any{ParamPhrases: []any{"тест"}, ParamUnit: unit, ParamMatch: MatchAny})
	}
	if !spec(UnitSubstring).Check("это тестирование").Satisfied {
		t.Fatal("substring must match inside a word")
	}
	if spec(UnitWord).Check("это тестирование").Satisfied {
		t.Fatal("whole word must not match inside a word")
	}
	if !spec(UnitWord).Check("это (тест), ок").Satisfied || !spec(UnitWord).Check("тест").Satisfied {
		t.Fatal("whole word must match between punctuation and at the edges")
	}
	if spec(UnitWord).Check("мега_тест тест1").Satisfied {
		t.Fatal("underscore and digits are word characters")
	}
	// A first occurrence that is not a word must not hide a later one.
	if !spec(UnitWord).Check("тестер и тест").Satisfied {
		t.Fatal("a later whole-word occurrence must be found")
	}
}

func TestCheckSectionScope(t *testing.T) {
	content := "# Spec\n\n## Цели\n\nнужен бюджет\n\n## Критерии приёмки:\n\nметрика X\n\n### Детали\n\nвнутри подраздела\n\n```\n## Не заголовок\n```\n\nещё текст\n\n## Риски\n\nбюджет риск\n"
	spec := func(phrase string) *Spec {
		return mustParse(t, map[string]any{ParamScope: ScopeSection, ParamSection: "критерии приёмки", ParamPhrases: []any{phrase}})
	}
	if !spec("метрика").Check(content).Satisfied {
		t.Fatal("phrase inside the section must be found (heading match ignores case and colon)")
	}
	if !spec("внутри подраздела").Check(content).Satisfied || !spec("ещё текст").Check(content).Satisfied {
		t.Fatal("subsections and fenced pseudo-headings belong to the section")
	}
	if spec("бюджет").Check(content).Satisfied {
		t.Fatal("a phrase from another section must not count")
	}
	missing := mustParse(t, map[string]any{ParamScope: ScopeSection, ParamSection: "Нет такого"}).Check(content)
	if missing.Satisfied || !strings.Contains(missing.Reason, "не найден") {
		t.Fatalf("a missing section must fail with a reason: %+v", missing)
	}
}

func TestCheckEmptyResultFails(t *testing.T) {
	for _, content := range []string{"", " \n\t "} {
		if result := mustParse(t, map[string]any{ParamMatch: MatchAny}).Check(content); result.Satisfied {
			t.Fatal("an empty result must never pass")
		}
	}
}

func TestCheckRegexIsExplicitRE2(t *testing.T) {
	spec := mustParse(t, map[string]any{
		ParamRegex: true, ParamPhrases: []any{`\bSLA\s+\d+%`}, ParamMatch: MatchAll, ParamCase: CaseSensitive,
	})
	if !spec.Check("SLA 99%").Satisfied || spec.Check("sla 99%").Satisfied || spec.Check("SLA high").Satisfied {
		t.Fatal("regex semantics (and case flag) are wrong")
	}
	insensitive := mustParse(t, map[string]any{ParamRegex: true, ParamPhrases: []any{`sla\s+\d+%`}})
	if !insensitive.Check("SLA 99%").Satisfied {
		t.Fatal("insensitive regex must ignore case")
	}
	// Without the flag the same text is a literal, not a pattern.
	literal := mustParse(t, map[string]any{ParamPhrases: []any{`\d+`}, ParamMatch: MatchAny})
	if literal.Check("123").Satisfied {
		t.Fatal("without regex: true a phrase is a literal")
	}
}

func TestSectionBodyHeadingSyntax(t *testing.T) {
	cases := map[string]struct {
		content string
		want    string
		found   bool
	}{
		"hash without space is not a heading": {"# Итог\nтекст\n#Другое\nещё\n", "текст\n#Другое\nещё\n", true},
		"indent 4 is not a heading":           {"# Итог\nтекст\n    # Другое\nещё\n", "текст\n    # Другое\nещё\n", true},
		"indent 3 is a heading":               {"# Итог\nтекст\n   # Другое\nещё\n", "текст\n", true},
		"seven hashes is not a heading":       {"# Итог\nтекст\n####### Другое\nещё\n", "текст\n####### Другое\nещё\n", true},
		"empty heading ends section":          {"## Итог\nтекст\n##\nещё\n", "текст\n", true},
		"closing hashes dropped":              {"## Итог ##\nтекст\n", "текст\n", true},
		"heading in html comment ignored":     {"# Итог\nтекст\n<!--\n# Другое\n-->\nещё\n", "текст\n<!--\n# Другое\n-->\nещё\n", true},
		"commented heading is not target":     {"<!--\n# Итог\n-->\nтекст\n", "", false},
		"setext level 1 ends section":         {"# Итог\nтекст\n\nДругое\n======\nещё\n", "текст\n\n", true},
		"setext target":                       {"Итог\n----\nтекст\n## Другое\nещё\n", "текст\n", true},
		"setext level 2 stays inside level 1": {"# Итог\nтекст\n\nПодраздел\n---\nещё\n", "текст\n\nПодраздел\n---\nещё\n", true},
		"thematic break is not setext":        {"# Итог\n\n---\nещё\n", "\n---\nещё\n", true},
		"closing fence needs no info":         {"# Итог\n```\n```go\n# Код\n```\nещё\n", "```\n```go\n# Код\n```\nещё\n", true},
		"shorter fence does not close":        {"# Итог\n````\n```\n# Код\n````\n# Другое\n", "````\n```\n# Код\n````\n", true},
		"other fence char does not close":     {"# Итог\n```\n~~~\n# Код\n```\n# Другое\n", "```\n~~~\n# Код\n```\n", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, found := sectionBody(tc.content, "Итог")
			if found != tc.found || got != tc.want {
				t.Fatalf("sectionBody() = %q, %v; want %q, %v", got, found, tc.want, tc.found)
			}
		})
	}
}

func TestParseRejectsZeroLengthRegex(t *testing.T) {
	for _, expression := range []string{`\b`, `a|\b`, `^`, `$`, `a*`, `(?:x)?`, `\B`} {
		_, err := Parse(params(map[string]any{ParamPhrases: []any{expression}, ParamRegex: true, ParamCase: CaseSensitive}))
		if err == nil {
			t.Errorf("выражение %q принято, хотя допускает нулевую длину", expression)
		}
	}
	if _, err := Parse(params(map[string]any{ParamPhrases: []any{`\bword\b`}, ParamRegex: true})); err != nil {
		t.Fatalf("корректное выражение отвергнуто: %v", err)
	}
}

func TestCheckCaseInsensitiveFoldsSigmaAndSharpS(t *testing.T) {
	cases := []struct{ phrase, content string }{
		{"ΟΔΥΣΣΕΥΣ", "Ο ΟΔΥΣΣΕΥς ήρθε"},
		{"οδυσσευς", "ΟΔΥΣΣΕΥΣ"},
		{"οδυσσεύς", "ΟΔΥΣΣΕΎΣ"},
		{"straße", "STRAẞE"},
		{"STRASSE", "STRASSE"},
	}
	for _, tc := range cases {
		spec := mustParse(t, map[string]any{ParamPhrases: []any{tc.phrase}})
		if !spec.Check(tc.content).Satisfied {
			t.Errorf("%q не найдена в %q", tc.phrase, tc.content)
		}
	}
	if mustParse(t, map[string]any{ParamPhrases: []any{"straße"}}).Check("STRASSE").Satisfied {
		t.Error("полная свёртка ß в ss не поддерживается и не должна срабатывать молча")
	}
	if _, err := Parse(params(map[string]any{ParamPhrases: []any{"σ", "ς"}})); err == nil {
		t.Error("σ и ς — дубликат при case: insensitive")
	}
}
