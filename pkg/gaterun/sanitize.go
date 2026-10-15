package gaterun

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/redact"
)

// minMaskedSecret is the shortest literal secret value masked verbatim. Very
// short values would shred unrelated text; pattern scanning still applies.
const minMaskedSecret = 4

// sanitizer removes secrets from everything the runner stores or returns:
// logs, reason, invocation record. Two layers: the shared pattern scanner of
// pkg/redact (tokens, keys, assignments, basic-auth URLs) and literal masking
// of the values of the allowed environment variables whose names look secret.
type sanitizer struct {
	literals []string
}

func newSanitizer(secretValues []string) sanitizer {
	seen := map[string]bool{}
	var literals []string
	for _, value := range secretValues {
		if len(value) < minMaskedSecret || seen[value] {
			continue
		}
		seen[value] = true
		literals = append(literals, value)
		// The same value inside a JSON string is escaped.
		if encoded, err := json.Marshal(value); err == nil {
			inner := string(encoded[1 : len(encoded)-1])
			if inner != value && !seen[inner] {
				seen[inner] = true
				literals = append(literals, inner)
			}
		}
	}
	sort.Slice(literals, func(i, j int) bool { return len(literals[i]) > len(literals[j]) })
	return sanitizer{literals: literals}
}

func (s sanitizer) text(in string) string {
	out := in
	findings := redact.Scan([]byte(in)).Findings
	sort.SliceStable(findings, func(i, j int) bool { return len(findings[i].Matched) > len(findings[j].Matched) })
	for _, finding := range findings {
		if finding.Matched != "" {
			out = strings.ReplaceAll(out, finding.Matched, finding.RedactedValue())
		}
	}
	for _, literal := range s.literals {
		out = strings.ReplaceAll(out, literal, "[REDACTED:env]")
	}
	return out
}

func (s sanitizer) bytes(in []byte) []byte { return []byte(s.text(string(in))) }

// leaks reports whether data still contains a secret. It is used for files the
// runner must not alter (evidence): those are rejected instead of rewritten.
func (s sanitizer) leaks(data []byte) bool {
	if len(redact.Scan(data).Findings) > 0 {
		return true
	}
	text := string(data)
	for _, literal := range s.literals {
		if strings.Contains(text, literal) {
			return true
		}
	}
	return false
}
