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

// Edge cases of the phrase list (S22). Each row pins a decision that the
// documentation states.
func TestParseEdgeCasesOfThePhraseList(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
		wantErr   bool
	}{
		{"empty phrase", map[string]any{ParamPhrases: []any{"alpha", ""}}, true},
		{"blank phrase", map[string]any{ParamPhrases: []any{"   "}}, true},
		{"no-break space only", map[string]any{ParamPhrases: []any{"\u00a0"}}, true},
		{"padded with a no-break space", map[string]any{ParamPhrases: []any{"alpha\u00a0"}}, true},
		{"carriage return inside", map[string]any{ParamPhrases: []any{"alpha\rbeta"}}, true},
		{"NUL inside", map[string]any{ParamPhrases: []any{"alpha\x00"}}, true},
		{"newline inside is literal", map[string]any{ParamPhrases: []any{"alpha\nbeta"}}, false},
		{"inner whitespace is literal", map[string]any{ParamPhrases: []any{"two  spaces"}}, false},
		{"duplicate ignoring case", map[string]any{ParamPhrases: []any{"Alpha", "ALPHA"}}, true},
		{"same word in different case is fine when sensitive", map[string]any{ParamPhrases: []any{"Alpha", "ALPHA"}, ParamCase: CaseSensitive}, false},
		{"overlap is fine for all", map[string]any{ParamPhrases: []any{"API", "API gateway"}}, false},
		{"overlap is fine for any", map[string]any{ParamPhrases: []any{"API", "API gateway"}, ParamMatch: MatchAny}, false},
		{"overlap is rejected for at_least", map[string]any{ParamPhrases: []any{"API", "API gateway"}, ParamMatch: MatchAtLeast, ParamMinCount: 2}, true},
		{"overlap ignoring case is rejected for at_least", map[string]any{ParamPhrases: []any{"api", "The API"}, ParamMatch: MatchAtLeast, ParamMinCount: 2}, true},
		{"case-different overlap is fine for sensitive at_least", map[string]any{ParamPhrases: []any{"api", "The API"}, ParamCase: CaseSensitive, ParamMatch: MatchAtLeast, ParamMinCount: 2}, false},
		{"disjoint phrases are fine for at_least", map[string]any{ParamPhrases: []any{"alpha", "beta"}, ParamMatch: MatchAtLeast, ParamMinCount: 2}, false},
		{"regex phrases are not compared for overlap", map[string]any{ParamPhrases: []any{"api", "api gateway"}, ParamRegex: true, ParamMatch: MatchAtLeast, ParamMinCount: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(params(tc.overrides))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				for _, phrase := range []string{"gateway", "alpha", "The API"} {
					if strings.Contains(err.Error(), phrase) {
						t.Fatalf("error repeats a phrase value: %v", err)
					}
				}
			}
		})
	}
}

func TestCheckOverlappingPhrasesCountEachEntryOnce(t *testing.T) {
	// One fragment of the text satisfies both entries; all and any report both
	// as found, at_least is refused at validation time (see above).
	spec := mustParse(t, params(map[string]any{ParamPhrases: []any{"API", "API gateway"}, ParamCase: CaseSensitive}))
	result := spec.Check("The API gateway is ready")
	if !result.Satisfied || len(result.Matched) != 2 {
		t.Fatalf("both entries are found in one fragment: %+v", result)
	}
	if got := spec.Check("The API is ready"); got.Satisfied || len(got.Missing) != 1 || got.Missing[0] != "API gateway" {
		t.Fatalf("the longer phrase must be reported missing: %+v", got)
	}
	// The same phrase occurring twice is still one entry.
	atLeast := mustParse(t, params(map[string]any{ParamPhrases: []any{"alpha", "beta"}, ParamMatch: MatchAtLeast, ParamMinCount: 2}))
	if atLeast.Check("alpha alpha alpha").Satisfied {
		t.Fatal("min_count counts distinct phrases, not occurrences")
	}
}

func TestCheckCaseRules(t *testing.T) {
	insensitive := mustParse(t, params(map[string]any{ParamPhrases: []any{"Срок"}}))
	sensitive := mustParse(t, params(map[string]any{ParamPhrases: []any{"Срок"}, ParamCase: CaseSensitive}))
	for content, want := range map[string][2]bool{
		"срок сдачи": {true, false},
		"СРОК":       {true, false},
		"Срок":       {true, true},
		"сроки":      {true, false}, // substring unit
	} {
		if got := insensitive.Check(content).Satisfied; got != want[0] {
			t.Errorf("insensitive %q = %v", content, got)
		}
		if got := sensitive.Check(content).Satisfied; got != want[1] {
			t.Errorf("sensitive %q = %v", content, got)
		}
	}
	// Simple case folding only: the Turkish dotted capital I is not "i".
	turkish := mustParse(t, params(map[string]any{ParamPhrases: []any{"istanbul"}}))
	if turkish.Check("\u0130stanbul").Satisfied {
		t.Error("U+0130 has no simple case folding to i and must not match silently")
	}
	// A regular expression follows the same case switch.
	pattern := mustParse(t, params(map[string]any{ParamPhrases: []any{"срок\\s+\\d+"}, ParamRegex: true}))
	if !pattern.Check("СРОК   10 дней").Satisfied {
		t.Error("case: insensitive applies to regex")
	}
	strict := mustParse(t, params(map[string]any{ParamPhrases: []any{"срок\\s+\\d+"}, ParamRegex: true, ParamCase: CaseSensitive}))
	if strict.Check("СРОК   10 дней").Satisfied {
		t.Error("case: sensitive applies to regex")
	}
}

func TestCheckUnicodeIsComparedWithoutNormalization(t *testing.T) {
	precomposed, decomposed := "caf\u00e9", "cafe\u0301"
	spec := mustParse(t, params(map[string]any{ParamPhrases: []any{precomposed}}))
	if !spec.Check("un " + precomposed + " noir").Satisfied {
		t.Fatal("identical form must match")
	}
	if spec.Check("un " + decomposed + " noir").Satisfied {
		t.Error("NFC and NFD are different texts: normalization is not applied, and a match must not appear by accident")
	}
	other := mustParse(t, params(map[string]any{ParamPhrases: []any{decomposed}}))
	if other.Check("un " + precomposed + " noir").Satisfied {
		t.Error("the reverse direction must behave the same")
	}
	// Both forms can be required explicitly through a regular expression.
	both := mustParse(t, params(map[string]any{ParamPhrases: []any{"caf(\u00e9|e\u0301)"}, ParamRegex: true}))
	if !both.Check(decomposed).Satisfied || !both.Check(precomposed).Satisfied {
		t.Error("an alternation covers both forms")
	}
	// A no-break space is not a space.
	space := mustParse(t, params(map[string]any{ParamPhrases: []any{"two words"}}))
	if space.Check("two\u00a0words").Satisfied {
		t.Error("NBSP must not match a space silently")
	}
	// The word boundary is Unicode-aware: a precomposed letter extends a word.
	word := mustParse(t, params(map[string]any{ParamPhrases: []any{"caf"}, ParamUnit: UnitWord}))
	if word.Check("caf\u00e9 ok").Satisfied {
		t.Error("é is a letter: caf is not a whole word in café")
	}
}

func TestCheckLineEndingsAndByteOrderMark(t *testing.T) {
	spec := mustParse(t, params(map[string]any{ParamPhrases: []any{"alpha\nbeta"}}))
	for _, content := range []string{"alpha\nbeta", "alpha\r\nbeta", "alpha\rbeta"} {
		if !spec.Check(content).Satisfied {
			t.Errorf("%q: line endings are unified before the comparison", content)
		}
	}
	section := mustParse(t, params(map[string]any{
		ParamPhrases: []any{"срок"}, ParamScope: ScopeSection, ParamSection: "Критерии приёмки",
	}))
	for name, content := range map[string]string{
		"lf":    "# Критерии приёмки\nсрок 5 дней\n",
		"crlf":  "# Критерии приёмки\r\nсрок 5 дней\r\n",
		"bom":   "\ufeff# Критерии приёмки\nсрок 5 дней\n",
		"mixed": "\ufeff# Критерии приёмки\r\nсрок 5 дней\r\n# Другое\r\n",
	} {
		if !section.Check(content).Satisfied {
			t.Errorf("%s: heading and body must be found", name)
		}
	}
	// A BOM in the middle is ordinary text and is not removed.
	if mustParse(t, params(map[string]any{ParamPhrases: []any{"alpha\ufeffbeta"}})).Check("alphabeta").Satisfied {
		t.Error("only a leading BOM is dropped")
	}
}

func TestCheckRepeatedSectionHeadingsAreSearchedTogether(t *testing.T) {
	spec := mustParse(t, params(map[string]any{
		ParamPhrases: []any{"first", "second"}, ParamScope: ScopeSection, ParamSection: "Notes",
	}))
	content := "# Notes\nfirst\n# Other\nnoise\n# notes:\nsecond\n"
	if result := spec.Check(content); !result.Satisfied {
		t.Fatalf("every section with that title is part of the scope: %+v", result)
	}
	if mustParse(t, params(map[string]any{ParamPhrases: []any{"noise"}, ParamScope: ScopeSection, ParamSection: "Notes"})).Check(content).Satisfied {
		t.Error("text of another section is outside the scope")
	}
}

func TestParseVersionOneKeepsPublishedValidation(t *testing.T) {
	for name, overrides := range map[string]map[string]any{
		"control character": {ParamPhrases: []any{"alpha\x1b"}},
		"nested at_least":   {ParamPhrases: []any{"API", "API gateway"}, ParamMatch: MatchAtLeast, ParamMinCount: 2},
	} {
		if _, err := ParseVersion(1, params(overrides)); err != nil {
			t.Errorf("%s: version 1 must accept it: %v", name, err)
		}
		if _, err := ParseVersion(2, params(overrides)); err == nil {
			t.Errorf("%s: version 2 must reject it", name)
		}
		if _, err := Parse(params(overrides)); err == nil {
			t.Errorf("%s: Parse uses the latest version", name)
		}
	}
	// Line ending and BOM normalization applies to both versions.
	spec, err := ParseVersion(1, params(map[string]any{ParamPhrases: []any{"alpha\nbeta"}}))
	if err != nil || !spec.Check("\ufeffalpha\r\nbeta").Satisfied {
		t.Fatalf("v1 normalizes line endings and BOM: %v", err)
	}
}
