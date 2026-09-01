// Package redact реализует контракт P1-6: классификация чувствительных полей,
// secrets-сканер, include/exclude policy и redaction evidence перед внешней
// публикацией. Скан консервативный (известные префиксы и assignment-паттерны):
// без ложных срабатываний на обычные слова; подтверждённые находки — повод
// для fail-closed блока export (флаг fail_export_on_secrets).
package redact

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// FieldClass — класс чувствительности поля (документированный контракт).
type FieldClass int

const (
	FieldPublic   FieldClass = iota // безопасно публиковать как есть
	FieldInternal                   // внутри организации, но не наружу
	FieldSecret                     // только в защищённых хранилищах, не наружу
)

func (c FieldClass) String() string {
	switch c {
	case FieldInternal:
		return "internal"
	case FieldSecret:
		return "secret"
	default:
		return "public"
	}
}

// ClassExpr — правило классификации имени поля по regexp-ключу.
type ClassExpr struct {
	KeyPattern string
	Class      FieldClass
}

// ClassifyField классифицирует имя поля (нижний регистр, подчёркивания
// нормализованы) по базовой таблице классов. Используется как
// документированный контракт: секретные поля нельзя наружу без redaction.
func ClassifyField(name string) FieldClass {
	key := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	key = strings.TrimSpace(key)
	for _, rule := range secretFieldRules {
		if rule.KeyPattern != "" && matchesKey(key, rule.KeyPattern) {
			return rule.Class
		}
	}
	return FieldPublic
}

var secretFieldRules = []ClassExpr{
	{KeyPattern: "password", Class: FieldSecret},
	{KeyPattern: "passwd", Class: FieldSecret},
	{KeyPattern: "secret", Class: FieldSecret},
	{KeyPattern: "token", Class: FieldSecret},
	{KeyPattern: "api_key", Class: FieldSecret},
	{KeyPattern: "apikey", Class: FieldSecret},
	{KeyPattern: "access_key", Class: FieldSecret},
	{KeyPattern: "private_key", Class: FieldSecret},
	{KeyPattern: "client_secret", Class: FieldSecret},
	{KeyPattern: "signing_key", Class: FieldSecret},
}

func matchesKey(name, pattern string) bool {
	if pattern == "password" {
		// точное слово или суффикс _password: избегаем matchesKey("ik_password")
		if name == pattern || strings.HasSuffix(name, "_"+pattern) {
			return true
		}
		return false
	}
	return name == pattern || strings.HasSuffix(name, "_"+pattern) ||
		strings.Contains(name, pattern)
}

// Finding — одно подтверждённое вхождение секрета в содержимом файла.
type Finding struct {
	Reason   string `json:"reason"`
	Matched  string `json:"matched"`
	Redacted string `json:"redacted"`
	Line     int    `json:"line"`
}

// RedactedValue возвращает безопасную замену для вставки вместо секрета.
func (f Finding) RedactedValue() string {
	return "[REDACTED:" + f.Reason + "]"
}

type secretRule struct {
	name       string
	regex      *regexp.Regexp
	valueGroup int // группа со значением секрета (-1 — нет); фильтр ложных срабатываний
}

var (
	secretOnce sync.Once
	secretRe   []secretRule
)

func compileSecretRules() []secretRule {
	secretOnce.Do(func() {
		patterns := []struct {
			name       string
			re         string
			valueGroup int
		}{
			{"private key", `-----BEGIN [A-Z ]*PRIVATE KEY-----`, -1},
			{"aws access key", `\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b`, -1},
			{"github token", `\bgh(?:p|o|u|s|r)_[0-9A-Za-z]{36,255}\b`, -1},
			{"openai key", `\bsk-(?:proj-|dev-|svc-|sess-)?[0-9A-Za-z]{16,}\b`, -1},
			{"slack token", `\bxox[abporsa]?-[0-9A-Za-z-]{10,}\b`, -1},
			{"google api key", `\bAIza[0-9A-Za-z\-_]{20,}\b`, -1},
			{"jwt", `\beyJ[0-9A-Za-z_-]{10,}\.[0-9A-Za-z_-]{10,}\.[0-9A-Za-z_-]{10,}\b`, -1},
			{"basic auth url", `\b[a-zA-Z][a-zA-Z0-9+.-]*://[^:/@\s]+:[^@\s]+@`, -1},
			{"secret assignment", `(?m)^\s*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?token|signing[_-]?key)\s*[:=]\s*["']?([0-9A-Za-z+/_\-.\=]{16,})`, 2},
		}
		for _, p := range patterns {
			re, err := regexp.Compile(p.re)
			if err != nil {
				panic(fmt.Sprintf("redact: invalid rule %s: %v", p.name, err))
			}
			secretRe = append(secretRe, secretRule{name: p.name, regex: re, valueGroup: p.valueGroup})
		}
	})
	return secretRe
}

// likelySecretValue фильтрует ложные срабатывания secret assignment:
// значения-плейсхолдеры, vault/env-ссылки и короткие слова без верхнего
// регистра и цифр не считаются секретом.
func likelySecretValue(value string) bool {
	value = strings.Trim(value, "\"' ")
	if len(value) < 16 {
		return false
	}
	if strings.ContainsAny(value, "<>") || strings.Contains(value, "${") ||
		strings.HasPrefix(value, "$") {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"example", "placeholder", "changeme", "your-", "dummy", "sample", "<insert"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	var hasUpper, hasDigit bool
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasUpper && hasDigit
}

// Scan возвращает все подтверждённые секретные вхождения в data. Результат
// стабильно упорядочен (по правилу, затем по позиции) для детерминизма.
func Scan(data []byte) []Finding {
	var findings []Finding
	for lineNumber, rawLine := range bytes.Split(data, []byte("\n")) {
		line := string(rawLine)
		for _, rule := range compileSecretRules() {
			if rule.valueGroup >= 0 {
				for _, loc := range rule.regex.FindAllStringSubmatchIndex(line, -1) {
					if len(loc) < rule.valueGroup+2 {
						continue
					}
					value := line[loc[rule.valueGroup]:loc[rule.valueGroup+1]]
					if !likelySecretValue(value) {
						continue
					}
					m := line[loc[0]:loc[1]]
					findings = append(findings, Finding{
						Reason:   rule.name,
						Matched:  m,
						Redacted: "[REDACTED:" + rule.name + "]",
						Line:     lineNumber + 1,
					})
				}
				continue
			}
			for _, loc := range rule.regex.FindAllStringIndex(line, -1) {
				m := line[loc[0]:loc[1]]
				findings = append(findings, Finding{
					Reason:   rule.name,
					Matched:  m,
					Redacted: "[REDACTED:" + rule.name + "]",
					Line:     lineNumber + 1,
				})
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		if findings[i].Reason != findings[j].Reason {
			return findings[i].Reason < findings[j].Reason
		}
		return findings[i].Matched < findings[j].Matched
	})
	return findings
}

// IsBinary грубо определяет, является ли содержимое бинарным (NUL в первых
// 4KiB). Бинарные файлы не сканируются.
func IsBinary(data []byte) bool {
	window := data
	if len(window) > 4096 {
		window = window[:4096]
	}
	return bytes.IndexByte(window, 0) >= 0
}

// ScanFile читает regular file (no-follow, через канонический safeio лимит)
// и сканирует его. Возвращает nil, nil для бинарных файлов.
func ScanFile(path string, maxBytes int64) ([]Finding, error) {
	data, err := safeio.ReadRegularFile(path, maxBytes)
	if err != nil {
		return nil, err
	}
	if IsBinary(data) {
		return nil, nil
	}
	return Scan(data), nil
}
