// Package scope implements repository-relative mutation path policies.
package scope

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Validate checks a portable repository-relative glob. Supported wildcards are
// *, ?, and ** (where ** may cross path separators).
func Validate(pattern string) error {
	if _, ok := normalize(pattern); !ok {
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
	// Значение нормализуется тем же кодом, что и паттерн: иначе
	// `docs/../secret.go` совпал бы с `docs/**`, то есть путь, физически
	// лежащий вне scope, прошёл бы политику. Вход, который после нормализации
	// покидает workspace, не совпадает ни с чем — fail-closed.
	normalized, ok := normalize(value)
	if !ok {
		return false
	}
	for _, pattern := range patterns {
		re, err := compile(pattern)
		if err == nil && re.MatchString(normalized) {
			return true
		}
	}
	return false
}

// normalize приводит repository-relative путь или glob к каноничному виду:
// Windows-разделители в '/', схлопывание './', '..' и повторных слэшей.
// Второй результат — false, если вход не может быть repository-relative:
// пустая строка, абсолютный путь или путь, выходящий за пределы workspace.
func normalize(value string) (string, bool) {
	slashed := strings.ReplaceAll(value, "\\", "/")
	if slashed == "" || path.IsAbs(slashed) {
		return "", false
	}
	cleaned := path.Clean(slashed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func compile(pattern string) (*regexp.Regexp, error) {
	pattern, ok := normalize(pattern)
	if !ok {
		return nil, fmt.Errorf("паттерн должен оставаться внутри workspace")
	}
	var expression strings.Builder
	// '^' и '$' — не косметика: без '^' паттерн совпадёт с любым суффиксом
	// пути (`docs/*` пропустит `evil/docs/x`), без '$' точное совпадение
	// выродится в префиксное (`cmd/main.go` пропустит `cmd/main.go.bak`).
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
			// Литерал берётся целой руной, а не байтом: `string(pattern[i])`
			// трактовал бы каждый байт UTF-8 как code point и превращал
			// `docs/й.md` в `^docs/Ð¹\.md$` — паттерн переставал совпадать
			// со своим же файлом и начинал совпадать с чужим.
			r, size := utf8.DecodeRuneInString(pattern[i:])
			if r == utf8.RuneError && size <= 1 {
				return nil, fmt.Errorf("невалидная UTF-8 последовательность в позиции %d", i)
			}
			expression.WriteString(regexp.QuoteMeta(string(r)))
			i += size
		}
	}
	expression.WriteByte('$')
	return regexp.Compile(expression.String())
}
