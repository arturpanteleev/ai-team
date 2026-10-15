package config

import (
	"strings"
	"testing"
)

// phrasesBody wraps gate params into a v6 stage body.
func phrasesBody(params string) string {
	return "    exit_gate_groups:\n      - - gate: required_phrases\n          version: 1\n          params: " + params + "\n"
}

func TestRequiredPhrasesConfigAcceptsExplicitParameters(t *testing.T) {
	for name, params := range map[string]string{
		"whole result": `{phrases: [метрика, SLA], case: insensitive, scope: result, match: all}`,
		"section":      `{phrases: [метрика], case: sensitive, scope: section, section: "Критерии приёмки", match: any, unit: word}`,
		"at least":     `{phrases: [a, b, c], case: insensitive, scope: result, match: at_least, min_count: 2}`,
		"regex":        `{phrases: ['\bSLA\s+\d+%'], case: sensitive, scope: result, match: all, regex: true}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := mustParse(t, v6Doc(phrasesBody(params)))
			if err := cfg.Validate(gateAgents()); err != nil {
				t.Fatalf("a valid configuration must be accepted: %v", err)
			}
		})
	}
}

func TestRequiredPhrasesConfigRejectsBadParametersBeforeRun(t *testing.T) {
	cases := []struct{ name, params, want string }{
		{"no case", `{phrases: [a], scope: result, match: all}`, `"case"`},
		{"no scope", `{phrases: [a], case: sensitive, match: all}`, `"scope"`},
		{"no match", `{phrases: [a], case: sensitive, scope: result}`, `"match"`},
		{"no phrases", `{case: sensitive, scope: result, match: all}`, `"phrases"`},
		{"empty phrases", `{phrases: [], case: sensitive, scope: result, match: all}`, `"phrases"`},
		{"bad case", `{phrases: [a], case: upper, scope: result, match: all}`, `"case"`},
		{"section without name", `{phrases: [a], case: sensitive, scope: section, match: all}`, `"section"`},
		{"section with result scope", `{phrases: [a], case: sensitive, scope: result, section: X, match: all}`, `"section"`},
		{"at_least without min", `{phrases: [a], case: sensitive, scope: result, match: at_least}`, `"min_count"`},
		{"min above phrase count", `{phrases: [a], case: sensitive, scope: result, match: at_least, min_count: 2}`, `"min_count"`},
		{"min without at_least", `{phrases: [a], case: sensitive, scope: result, match: all, min_count: 1}`, `"min_count"`},
		{"broken regex", `{phrases: ['(oops'], case: sensitive, scope: result, match: all, regex: true}`, "RE2"},
		{"backtracking construct", `{phrases: ['a(?=b)'], case: sensitive, scope: result, match: all, regex: true}`, "RE2"},
		{"empty-matching regex", `{phrases: ['a*'], case: sensitive, scope: result, match: all, regex: true}`, "пустой строкой"},
		{"regex with word unit", `{phrases: [a], case: sensitive, scope: result, match: all, regex: true, unit: word}`, `"unit"`},
		{"script param", `{phrases: [a], case: sensitive, scope: result, match: all, script: "x"}`, "script"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseYAML([]byte(v6Doc(phrasesBody(tc.params))))
			if err == nil {
				err = cfg.Validate(gateAgents())
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want configuration error containing %q, got %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "stages[0] (product_spec): exit_gate_groups[0][0]") {
				t.Fatalf("error must name the stage and the gate path: %v", err)
			}
		})
	}
}

func TestRequiredPhrasesIsMarkdownOnly(t *testing.T) {
	doc := `schema_version: 6
template: gates
title: Gates
stages:
  - id: implementation
    title: Impl
    function: developer
    result: link
    link_kind: pr
` + phrasesBody(`{phrases: [a], case: sensitive, scope: result, match: all}`) + `  - id: done
    title: Done
    function: bo
    result: approve
`
	cfg := mustParse(t, doc)
	err := cfg.Validate(gateAgents())
	if err == nil || !strings.Contains(err.Error(), "применим только к result md") {
		t.Fatalf("required_phrases on a link result must be rejected: %v", err)
	}
}
