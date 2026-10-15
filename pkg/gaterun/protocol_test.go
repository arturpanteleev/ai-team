package gaterun

import (
	"strings"
	"testing"
)

func TestParseResultAccepts(t *testing.T) {
	good := []string{
		`{"protocol":"ai-team.gate/v1","verdict":"pass"}`,
		`{"protocol":"ai-team.gate/v1","verdict":"not_applicable","reason":"x"}`,
		`{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"missing section","evidence":[{"kind":"report","path":"a/b.txt","sha256":"` + strings.Repeat("a", 64) + `"}]}`,
	}
	for _, doc := range good {
		if _, err := ParseResult([]byte(doc), 1<<20); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
}

func TestParseResultRejects(t *testing.T) {
	cases := map[string]string{
		"empty":            ``,
		"not json":         `all good`,
		"two documents":    `{"protocol":"ai-team.gate/v1","verdict":"pass"}{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"x"}`,
		"trailing garbage": `{"protocol":"ai-team.gate/v1","verdict":"pass"} nope`,
		"wrong protocol":   `{"protocol":"v0","verdict":"pass"}`,
		"no protocol":      `{"verdict":"pass"}`,
		"unknown verdict":  `{"protocol":"ai-team.gate/v1","verdict":"ok"}`,
		"empty verdict":    `{"protocol":"ai-team.gate/v1","verdict":""}`,
		"unknown field":    `{"protocol":"ai-team.gate/v1","verdict":"pass","extra":1}`,
		"fail no reason":   `{"protocol":"ai-team.gate/v1","verdict":"fail"}`,
		"long reason":      `{"protocol":"ai-team.gate/v1","verdict":"pass","reason":"` + strings.Repeat("x", MaxReasonBytes+1) + `"}`,
		"abs evidence":     `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"/etc/passwd"}]}`,
		"dotdot evidence":  `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"../x"}]}`,
		"unclean evidence": `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"a//b"}]}`,
		"backslash":        `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"a\\b"}]}`,
		"bad kind":         `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"Log!","path":"a"}]}`,
		"bad sha":          `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"a","sha256":"zz"}]}`,
		"dup path":         `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"log","path":"a"},{"kind":"log","path":"a"}]}`,
	}
	for name, doc := range cases {
		if _, err := ParseResult([]byte(doc), 1<<20); err == nil {
			t.Errorf("%s: accepted %s", name, doc)
		}
	}
	if _, err := ParseResult([]byte(`{"protocol":"ai-team.gate/v1","verdict":"pass"}`), 10); err == nil {
		t.Error("document over the limit accepted")
	}
}

func TestParseResultTooManyEvidence(t *testing.T) {
	var parts []string
	for i := 0; i <= MaxEvidenceRefs; i++ {
		parts = append(parts, `{"kind":"log","path":"f`+strings.Repeat("a", i)+`"}`)
	}
	doc := `{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[` + strings.Join(parts, ",") + `]}`
	if _, err := ParseResult([]byte(doc), 1<<20); err == nil {
		t.Fatal("too many evidence refs accepted")
	}
}

func TestInputValidate(t *testing.T) {
	in := Input{
		Protocol: Protocol, TaskID: "t", Stage: "s", Visit: 1, AttemptID: "a-1",
		Gate:     GateInfo{ID: "external_program", Version: 1, Name: "x"},
		Artifact: ArtifactInfo{Type: "md", SHA256: strings.Repeat("0", 64)},
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	in.AttemptID = "../evil"
	if in.Validate() == nil {
		t.Fatal("unsafe attempt id accepted")
	}
}
