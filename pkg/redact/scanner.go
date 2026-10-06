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

// maxJSONDepth — предел вложенности, глубже которого поддерево НЕ разбирается
// (QS-08): защита от злонамеренно глубоких документов. Превышение лимита не
// прекращает обход — поддерево пропускается, факт пропуска записывается в
// Result.Unscanned, остальной документ сканируется дальше.
//
// Прежний лимит на размер документа (4 MiB) снят: данные уже целиком в памяти
// (ScanFile ограничивает чтение MaxScanFileBytes), а обход стал линейным —
// см. lineTracker. Лимит не экономил ничего, но делал любой документ крупнее
// 4 MiB слепой зоной fail-closed блокера.
const maxJSONDepth = 16

// Причины, по которым участок содержимого остался непросканированным (QS-08).
const (
	unscannedJSONDepth = "json depth limit"
	unscannedJSONParse = "json parse truncated"
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

// Unscanned — участок содержимого, который сканер разобрать не смог (QS-08).
//
// Почему это часть результата, а не молчаливый выход: сканер — fail-closed
// блокер экспорта. «Находок нет» и «я не смотрел» — разные вердикты, и
// механизм, который выдаёт второе за первое, опаснее отсутствующего. Всё, что
// не просканировано, обязано дойти до отчёта и до решения о публикации.
type Unscanned struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Line   int    `json:"line"`
}

// Result — итог скана содержимого: подтверждённые находки плюс честный
// перечень того, что осталось непросканированным.
type Result struct {
	Findings  []Finding   `json:"findings,omitempty"`
	Unscanned []Unscanned `json:"unscanned,omitempty"`
}

// Complete — обход дошёл до конца: слепых зон нет. Только при true «находок
// нет» означает «секретов нет».
func (r Result) Complete() bool { return len(r.Unscanned) == 0 }

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
			// (?i) на альтернацию ключей (QS-08/#150): PASSWORD=, API_KEY=,
			// Token: — доминирующая форма в env-файлах, shell-export'ах и
			// CI-логах, то есть ровно в том, что попадает в evidence. Флаг
			// ограничен группой ключей: значение остаётся в явном классе
			// символов, поведение остальных правил не меняется. Ложные
			// срабатывания отсекает likelySecretValue, а не регистр ключа.
			// Необязательные кавычки вокруг ключа: JSON/YAML-фрагмент в
			// логе («  "api_key": "…"») построчным правилом иначе не
			// ловится, а JSON-сканер на таком куске не работает — строка
			// сама по себе не валидный документ.
			{"secret assignment", `(?m)^\s*["']?(?i:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?token|signing[_-]?key)["']?\s*[:=]\s*["']?([0-9A-Za-z+/_\-.\=]{16,})`, 2},
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
// в одну ли строку записан документ. Для не-JSON содержимого возвращается
// nil — plain-сканер покрывает остальное.
//
// QS-08: обход НИКОГДА не заканчивается молча. Поддерево глубже maxJSONDepth
// пропускается (а не обрывает весь документ), обрыв структуры внутри
// контейнера фиксируется — и то, и другое уходит во второй результат как
// «не просканировано».
func scanJSON(data []byte) ([]Finding, []Unscanned) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	lines := newLineTracker(data)
	var stack []jsonFrame
	var findings []Finding
	var gaps []Unscanned
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				// EOF при непустом стеке — документ оборван на середине:
				// json.Decoder отдаёт io.EOF и на незакрытых контейнерах.
				if len(stack) > 0 {
					gaps = append(gaps, Unscanned{
						Reason: unscannedJSONParse,
						Detail: "документ оборван: контейнер не закрыт",
						Line:   lines.at(decoder.InputOffset()),
					})
				}
				break
			}
			// Ошибка при пустом стеке — это просто не-JSON содержимое
			// (обычный лог, код, markdown): сканировать структурно нечего,
			// plain-правила уже отработали. Ошибка ВНУТРИ контейнера — другое
			// дело: документ начинался как JSON и оборвался, значит остаток
			// действительно не разобран и это обязано быть видно.
			if len(stack) > 0 {
				gaps = append(gaps, Unscanned{
					Reason: unscannedJSONParse,
					Detail: err.Error(),
					Line:   lines.at(decoder.InputOffset()),
				})
			}
			break
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
				if top.keySecret || top.secretCtx {
					if likelySecretValue(value) {
						findings = append(findings, Finding{
							Reason:   jsonSecretReason,
							Matched:  value,
							Redacted: jsonSecretRedacted,
							Line:     lines.at(top.keyOffset),
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
							Line:     lines.at(offset),
						})
					}
				}
			}
		case json.Delim:
			switch value {
			case '{', '[':
				inherited := false
				if len(stack) > 0 {
					top := &stack[len(stack)-1]
					// контейнер как значение ключа или элемент объекта/
					// массива наследует секретный контекст родителя.
					inherited = top.keySecret || top.secretCtx
					top.keySecret = false
					top.expectKey = true
				}
				if len(stack) >= maxJSONDepth {
					// Лимит глубины срабатывает на ПОДДЕРЕВО, а не на
					// документ: дочитываем его до закрывающего токена и
					// продолжаем с того же уровня. Родительский фрейм уже
					// переведён в состояние «значение получено» выше.
					line := lines.at(offset)
					skipErr := skipSubtree(decoder)
					gaps = append(gaps, Unscanned{
						Reason: unscannedJSONDepth,
						Detail: fmt.Sprintf("вложенность глубже %d: поддерево не просканировано", maxJSONDepth),
						Line:   line,
					})
					if skipErr != nil {
						gaps = append(gaps, Unscanned{
							Reason: unscannedJSONParse,
							Detail: skipErr.Error(),
							Line:   lines.at(decoder.InputOffset()),
						})
						return findings, gaps
					}
					continue
				}
				stack = append(stack, jsonFrame{
					isObject:  value == '{',
					expectKey: value == '{',
					secretCtx: inherited,
				})
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
	}
	return findings, gaps
}

// skipSubtree дочитывает токены уже открытого контейнера до его закрытия.
// Используется, когда поддерево глубже лимита: вместо отказа от всего
// документа сканер «перешагивает» такое поддерево.
func skipSubtree(decoder *json.Decoder) error {
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// lineTracker переводит byte offset в 1-based номер строки за линейное время
// на весь обход. Прежний lineAt считал переводы строк от начала документа для
// КАЖДОЙ находки — O(n²), и именно этим оправдывался лимит на размер
// JSON-документа. Offsets декодера монотонно растут, поэтому достаточно
// досчитывать newline'ы от предыдущей позиции.
type lineTracker struct {
	data   []byte
	offset int64
	line   int
}

func newLineTracker(data []byte) *lineTracker {
	return &lineTracker{data: data, line: 1}
}

func (t *lineTracker) at(offset int64) int {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(t.data)) {
		offset = int64(len(t.data))
	}
	if offset < t.offset {
		// Редкий случай (offset назад): считаем честно с начала, курсор не
		// сдвигаем — корректность важнее экономии.
		return lineAt(t.data, offset)
	}
	t.line += bytes.Count(t.data[t.offset:offset], []byte("\n"))
	t.offset = offset
	return t.line
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

// Scan возвращает все подтверждённые секретные вхождения в data и перечень
// участков, которые разобрать не удалось. Результат стабильно упорядочен
// (по позиции, затем по правилу) для детерминизма. Помимо line-based правил
// сканируются структурные JSON-поля (AUD-03).
//
// QS-08: Scan возвращает Result, а не []Finding, намеренно — пустой список
// находок сам по себе ничего не доказывает, пока вызывающий не увидел
// Result.Unscanned. Старая сигнатура позволяла потерять этот факт по дороге.
func Scan(data []byte) Result {
	var findings []Finding
	var gaps []Unscanned
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
	if len(data) > 0 {
		jsonFindings, jsonGaps := scanJSON(data)
		findings = append(findings, jsonFindings...)
		gaps = append(gaps, jsonGaps...)
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
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Line != gaps[j].Line {
			return gaps[i].Line < gaps[j].Line
		}
		if gaps[i].Reason != gaps[j].Reason {
			return gaps[i].Reason < gaps[j].Reason
		}
		return gaps[i].Detail < gaps[j].Detail
	})
	return Result{Findings: findings, Unscanned: gaps}
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
// и сканирует его. Для бинарных файлов возвращает пустой Result (пропуск по
// контракту, а не слепая зона лимита). maxBytes<=0 означает канонический
// дефолт MaxScanFileBytes (как и в ScanDir/Verify).
func ScanFile(path string, maxBytes int64) (Result, error) {
	if maxBytes <= 0 {
		maxBytes = MaxScanFileBytes
	}
	data, err := safeio.ReadRegularFile(path, maxBytes)
	if err != nil {
		return Result{}, err
	}
	if IsBinary(data) {
		return Result{}, nil
	}
	return Scan(data), nil
}
