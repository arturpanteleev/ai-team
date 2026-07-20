// Package verdict — машиночитаемый контракт вердиктов и статусов агентов.
//
// Канонические маркеры (line-anchored, отдельной строкой в артефакте):
//
//	**Verdict:** APPROVED | CHANGES_REQUESTED | REJECTED
//	**Result:** PASS | FAIL
//
// Сигнал блокировки — файл {feature}/status/{agent}.md:
//
//	**Status:** BLOCKED
//	**Blocker:** <причина>
//
// Промпт-инструкции для агентов генерирует PromptInstructions — единственный
// источник формата для LLM; contract-тесты проверяют, что парсер понимает
// ровно то, что требует инструкция.
package verdict

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type Verdict string

// Contract описывает обязательный control-channel конкретной роли.
// Marker — Verdict или Result; Values — допустимые значения.
type Contract struct {
	Required bool      `yaml:"required,omitempty"`
	Marker   string    `yaml:"marker,omitempty"`
	Values   []Verdict `yaml:"values,omitempty"`
}

const (
	None             Verdict = ""
	Approved         Verdict = "APPROVED"
	ChangesRequested Verdict = "CHANGES_REQUESTED"
	Rejected         Verdict = "REJECTED"
	Pass             Verdict = "PASS"
	Fail             Verdict = "FAIL"
)

var (
	verdictRe = regexp.MustCompile(`(?m)^\*\*Verdict:\*\*\s+(APPROVED|CHANGES_REQUESTED|REJECTED)\s*$`)
	resultRe  = regexp.MustCompile(`(?m)^\*\*Result:\*\*\s+(PASS|FAIL)\s*$`)
	blockedRe = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+BLOCKED\s*$`)
	blockerRe = regexp.MustCompile(`(?m)^\*\*Blocker:\*\*\s+(.+)$`)
)

// IsNegative — вердикты, при которых enforcement останавливает пайплайн.
func (v Verdict) IsNegative() bool {
	switch v {
	case Rejected, ChangesRequested, Fail:
		return true
	}
	return false
}

// Parse возвращает первый канонический вердикт в тексте (line-anchored).
// Упоминания в прозе не совпадают: маркер должен занимать отдельную строку.
func Parse(content string) Verdict {
	vLoc := verdictRe.FindStringSubmatchIndex(content)
	rLoc := resultRe.FindStringSubmatchIndex(content)
	switch {
	case vLoc == nil && rLoc == nil:
		return None
	case vLoc == nil:
		return Verdict(content[rLoc[2]:rLoc[3]])
	case rLoc == nil:
		return Verdict(content[vLoc[2]:vLoc[3]])
	case vLoc[0] < rLoc[0]:
		return Verdict(content[vLoc[2]:vLoc[3]])
	default:
		return Verdict(content[rLoc[2]:rLoc[3]])
	}
}

// ParseFile читает файл и парсит вердикт; отсутствие файла — None.
func ParseFile(path string) Verdict {
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil {
		return None
	}
	return Parse(string(data))
}

// FromOutputs сканирует .md-файлы выходов этапа и возвращает первый вердикт.
func FromOutputs(paths []string) Verdict {
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		if v := ParseFile(p); v != None {
			return v
		}
	}
	return None
}

// FromOutputsContract строго валидирует verdict-bearing stage. В отличие от
// FromOutputs, отсутствие, неизвестное значение и несколько маркеров являются
// ошибками контракта, а порядок outputs детерминирован.
func FromOutputsContract(paths []string, contract *Contract) (Verdict, error) {
	if contract == nil {
		return FromOutputs(paths), nil
	}
	if err := contract.Validate(); err != nil {
		return None, err
	}

	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	pattern := regexp.MustCompile(`(?m)^\*\*(Verdict|Result):\*\*\s+([^\s]+)\s*$`)
	type match struct {
		path   string
		marker string
		value  Verdict
	}
	var matches []match
	for _, path := range sorted {
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			continue
		}
		data, err := safeio.ReadRegularFile(path, 8<<20)
		if err != nil {
			return None, fmt.Errorf("verdict contract: не удалось прочитать %s: %w", path, err)
		}
		for _, parts := range pattern.FindAllStringSubmatch(string(data), -1) {
			matches = append(matches, match{path: path, marker: parts[1], value: Verdict(parts[2])})
		}
	}

	if len(matches) == 0 {
		return None, fmt.Errorf("verdict contract: обязательный маркер **%s:** отсутствует", contract.Marker)
	}
	if len(matches) > 1 {
		return None, fmt.Errorf("verdict contract: найдено несколько control-маркеров Verdict/Result (%d)", len(matches))
	}
	if matches[0].marker != contract.Marker {
		return None, fmt.Errorf("verdict contract: ожидался маркер **%s:**, найден **%s:** в %s", contract.Marker, matches[0].marker, matches[0].path)
	}
	if !contract.Allows(matches[0].value) {
		return None, fmt.Errorf("verdict contract: значение %q из %s недопустимо", matches[0].value, matches[0].path)
	}
	return matches[0].value, nil
}

func (c *Contract) Validate() error {
	if c == nil {
		return nil
	}
	if !c.Required {
		return fmt.Errorf("verdict contract: required должен быть true")
	}
	if c.Marker != "Verdict" && c.Marker != "Result" {
		return fmt.Errorf("verdict contract: marker должен быть Verdict или Result")
	}
	if len(c.Values) == 0 {
		return fmt.Errorf("verdict contract: values не могут быть пустыми")
	}
	seen := make(map[Verdict]bool, len(c.Values))
	for _, value := range c.Values {
		if seen[value] {
			return fmt.Errorf("verdict contract: значение %s дублируется", value)
		}
		seen[value] = true
		switch c.Marker {
		case "Verdict":
			if value != Approved && value != ChangesRequested && value != Rejected {
				return fmt.Errorf("verdict contract: значение %q несовместимо с marker Verdict", value)
			}
		case "Result":
			if value != Pass && value != Fail {
				return fmt.Errorf("verdict contract: значение %q несовместимо с marker Result", value)
			}
		default:
			return fmt.Errorf("verdict contract: неизвестное значение %q", value)
		}
	}
	return nil
}

func (c *Contract) Allows(value Verdict) bool {
	for _, allowed := range c.Values {
		if value == allowed {
			return true
		}
	}
	return false
}

// StatusFilePath — путь status-файла агента.
func StatusFilePath(artifactRoot, feature, agent string) string {
	return filepath.Join(artifactRoot, feature, "status", agent+".md")
}

// ReadBlocked проверяет status-файл агента на сигнал BLOCKED.
func ReadBlocked(artifactRoot, feature, agent string) (bool, string) {
	return ReadBlockedSince(artifactRoot, feature, agent, time.Time{})
}

// ReadBlockedSince игнорирует status-файл, созданный до текущей попытки.
func ReadBlockedSince(artifactRoot, feature, agent string, since time.Time) (bool, string) {
	path := StatusFilePath(artifactRoot, feature, agent)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !since.IsZero() && info.ModTime().Before(since) {
		return false, ""
	}
	data, err := safeio.ReadRegularFile(path, 64<<10)
	if err != nil {
		return false, ""
	}
	content := string(data)
	if !blockedRe.MatchString(content) {
		return false, ""
	}
	reason := "причина не указана"
	if m := blockerRe.FindStringSubmatch(content); m != nil {
		reason = strings.TrimSpace(m[1])
	}
	return true, reason
}

// VerdictInstruction — фрагмент промпта с требованием канонического формата
// вердикта. values — допустимые значения для конкретной роли.
func VerdictInstruction(marker string, values ...Verdict) string {
	strs := make([]string, len(values))
	for i, v := range values {
		strs[i] = string(v)
	}
	return fmt.Sprintf("Последней строкой артефакта укажи вердикт строго в формате (отдельная строка, без другого текста):\n**%s:** %s",
		marker, strings.Join(strs, " | "))
}

// BlockedInstruction — фрагмент служебной секции промпта: как сигналить BLOCKED.
func BlockedInstruction(artifactRoot, feature, agent string) string {
	return fmt.Sprintf(`Если задачу невозможно выполнить (противоречивые или существенно неполные требования) — НЕ создавай обычные выходные файлы, а создай файл %s с содержимым:
**Status:** BLOCKED
**Blocker:** <краткая причина>`, StatusFilePath(artifactRoot, feature, agent))
}
