// Package redact реализует контракт P1-6: классификация чувствительных полей,
// secrets-сканер, include/exclude policy и redaction evidence перед внешней
// публикацией. Скан консервативный (известные префиксы и assignment-паттерны):
// без ложных срабатываний на обычные слова; подтверждённые находки — повод
// для fail-closed блока export (флаг fail_export_on_secrets).
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// jsonSecretReason — причина находки для секретных полей в JSON/JSONL.
const jsonSecretReason = "json secret field"

// jsonSecretRedacted — immutable redaction evidence (AUD-03: "password",
// "secret", "token", "api_key", "private_key" и пр. поля, чьи значения похожи
// на реальные секреты, должны обнаруживаться в JSON/JSONL-структурах также,
// как и в plain-тексте).
const jsonSecretRedacted = "[REDACTED:json secret field]"

// Лимиты JSON-примеси для сканера (AUD-03): защита от злонамеренно глубоких/
// больших JSON-документов при детерминированной сортировке evidence.
const (
	maxJSONScanBytes = 4 << 20 // 4 MiB
	maxJSONDepth     = 16
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

// beginPrivateKeyRe / endPrivateKeyRe — границы PEM-блока private key.
// RedactFile вырезает весь блок (BEGIN … END), а не только BEGIN-строку:
// иначе base64-тело ключа оставалось бы в redacted-копии.
var (
	beginPrivateKeyOnce  sync.Once
	beginPrivateKeyReVal *regexp.Regexp
	endPrivateKeyReVal   *regexp.Regexp
)

func beginPrivateKeyRe() *regexp.Regexp {
	beginPrivateKeyOnce.Do(func() {
		beginPrivateKeyReVal = regexp.MustCompile(`(?m)^[ \t]*-----BEGIN [A-Z ]*PRIVATE KEY-----$`)
		endPrivateKeyReVal = regexp.MustCompile(`(?m)^[ \t]*-----END [A-Z ]*PRIVATE KEY-----$`)
	})
	return beginPrivateKeyReVal
}

func endPrivateKeyRe() *regexp.Regexp {
	beginPrivateKeyRe()
	return endPrivateKeyReVal
}

// likelySecretValue фильтрует ложные срабатывания secret assignment:
// значения-плейсхолдеры, vault/env-ссылки и обычные слова без цифр не
// считаются секретом. Токен обязан содержать и букву, и цифру (покрывает
// hex/base64/lower-токены, которые раньше терялись из-за требования верхнего
// регистра — F-7); чистые слова остаются benign.
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
	var hasLetter, hasDigit bool
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// jsonFrame — контейнер в стеке декодера. keySecret/expectKey используются
// только для object-фреймов; для array-фреймов значим только факт вложенности.
// secretCtx наследуется родителем и означает, что значения этого контейнера
// находятся в секретном дереве (значение секретного ключа или элемент
// секретного массива) — позволяет не терять контекст на массивах (F-7).
type jsonFrame struct {
	isObject  bool
	expectKey bool // в object контексте следующий string — имя ключа
	keySecret bool // последний ключ object'а классифицирован как secret
	keyOffset int64
	secretCtx bool
}

// scanJSON детектирует секретные JSON-поля через токенную экскурсию по
// документу (AUD-03). Finder работает структурно и не зависит от того,
// в одну ли строку записан документ. Для не-JSON содержимого (и данных вне
// bounds) возвращается nil — plain-сканер покрывает остальное.
func scanJSON(data []byte) []Finding {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var stack []jsonFrame
	var findings []Finding
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return findings
		}
		offset := decoder.InputOffset()
		switch value := token.(type) {
		case string:
			if len(stack) == 0 {
				continue
			}
			top := &stack[len(stack)-1]
			if top.isObject && !top.expectKey {
				// value: значение ключа top-объекта.
				if (top.keySecret || top.secretCtx) && top.keyOffset >= 0 && top.keyOffset < int64(len(data)) {
					if likelySecretValue(value) {
						findings = append(findings, Finding{
							Reason:   jsonSecretReason,
							Matched:  value,
							Redacted: jsonSecretRedacted,
							Line:     lineAt(data, top.keyOffset),
						})
					}
				}
				top.keySecret = false
				top.expectKey = true
			} else if top.isObject {
				// string в роли ключа: фиксируем классификацию имени поля.
				top.expectKey = false
				top.keySecret = ClassifyField(value) == FieldSecret
				top.keyOffset = offset
			} else {
				// элемент массива: секретный контекст наследован от ключа.
				if top.secretCtx {
					if likelySecretValue(value) {
						findings = append(findings, Finding{
							Reason:   jsonSecretReason,
							Matched:  value,
							Redacted: jsonSecretRedacted,
							Line:     lineAt(data, offset),
						})
					}
				}
			}
		case json.Delim:
			switch value {
			case '{':
				inherited := false
				if len(stack) > 0 {
					top := &stack[len(stack)-1]
					// объект как значение ключа или элемент объекта/массива
					// наследует секретный контекст родителя.
					inherited = top.keySecret || top.secretCtx
					top.keySecret = false
					top.expectKey = true
				}
				stack = append(stack, jsonFrame{isObject: true, expectKey: true, secretCtx: inherited})
			case '[':
				inherited := false
				if len(stack) > 0 {
					top := &stack[len(stack)-1]
					inherited = top.keySecret || top.secretCtx
					top.keySecret = false
					top.expectKey = true
				}
				stack = append(stack, jsonFrame{secretCtx: inherited})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if len(stack) > 0 && stack[len(stack)-1].isObject {
					stack[len(stack)-1].expectKey = true
				}
			}
		default:
			// bool/number/null — значение нестроковое, секрета нет.
			if len(stack) > 0 {
				stack[len(stack)-1].keySecret = false
				if stack[len(stack)-1].isObject {
					stack[len(stack)-1].expectKey = true
				}
			}
		}
		if len(stack) > maxJSONDepth {
			return findings
		}
	}
	return findings
}

// lineAt возвращает 1-based номер строки для byte offset (AUD-03).
func lineAt(data []byte, offset int64) int {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	return 1 + bytes.Count(data[:int(offset)], []byte("\n"))
}

// Scan возвращает все подтверждённые секретные вхождения в data. Результат
// стабильно упорядочен (по правилу, затем по позиции) для детерминизма.
// Помимо line-based правил сканируются структурные JSON-поля (AUD-03).
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
	if len(data) > 0 && int64(len(data)) <= maxJSONScanBytes {
		findings = append(findings, scanJSON(data)...)
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
// и сканирует его. Возвращает nil, nil для бинарных файлов. maxBytes<=0
// означает канонический дефолт MaxScanFileBytes (как и в ScanDir/Verify).
func ScanFile(path string, maxBytes int64) ([]Finding, error) {
	if maxBytes <= 0 {
		maxBytes = MaxScanFileBytes
	}
	data, err := safeio.ReadRegularFile(path, maxBytes)
	if err != nil {
		return nil, err
	}
	if IsBinary(data) {
		return nil, nil
	}
	return Scan(data), nil
}
