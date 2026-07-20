// Package scope implements repository-relative mutation path policies.
package scope

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Validate checks a portable repository-relative glob. Supported wildcards are
// *, ?, and ** (where ** may cross path separators).
func Validate(pattern string) error {
	normalized := strings.ReplaceAll(pattern, "\\", "/")
	cleaned := path.Clean(normalized)
	if pattern == "" || path.IsAbs(normalized) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("scope pattern %q должен оставаться внутри workspace", pattern)
	}
	if _, err := compile(pattern); err != nil {
		return fmt.Errorf("scope pattern %q: %w", pattern, err)
	}
	return nil
}

// MatchAny reports whether a repository-relative slash-separated path is
// allowed by at least one pattern.
func MatchAny(patterns []string, value string) bool {
	value = strings.TrimPrefix(strings.ReplaceAll(value, "\\", "/"), "./")
	for _, pattern := range patterns {
		re, err := compile(pattern)
		if err == nil && re.MatchString(value) {
			return true
		}
	}
	return false
}

func compile(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimPrefix(strings.ReplaceAll(pattern, "\\", "/"), "./")
	var expression strings.Builder
	expression.WriteByte('^')
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					expression.WriteString("(?:.*/)?")
					i++
				} else {
					expression.WriteString(".*")
				}
				continue
			}
			expression.WriteString("[^/]*")
			i++
		case '?':
			expression.WriteString("[^/]")
			i++
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	expression.WriteByte('$')
	return regexp.Compile(expression.String())
}
