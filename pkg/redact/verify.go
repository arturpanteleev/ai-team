package redact

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// Violation — fail-closed причина блокировки внешней публикации.
type Violation struct {
	Path     string    `json:"path"`
	Findings []Finding `json:"findings"`
}

func (v Violation) Error() string {
	kinds := make([]string, 0, len(v.Findings))
	seen := map[string]bool{}
	for _, f := range v.Findings {
		if !seen[f.Reason] {
			kinds = append(kinds, f.Reason)
			seen[f.Reason] = true
		}
	}
	return fmt.Sprintf("%d secret-находок (%s)", len(v.Findings), strings.Join(kinds, ", "))
}

// FileResult — находки в одном файле относительно корня сканирования.
type FileResult struct {
	Path     string    `json:"path"`
	Findings []Finding `json:"findings"`
}

// Report — детерминированный (стабильно упорядоченный) результат проверки.
type Report struct {
	Verdict    string      `json:"verdict"`
	Files      int         `json:"files_scanned"`
	Bytes      int64       `json:"bytes_scanned"`
	Violations []Violation `json:"violations,omitempty"`
}

// ScanDir обходит regular файлы под root (без симлинков, без special files;
// бинарные пропускаются) и собирает находки по policy. Символьные ссылки не
// разыменовываются и не обходятся (WalkDir не следует ссылкам).
func ScanDir(root string, policy Policy) (found []FileResult, scanned int, total int64, err error) {
	policy = policy.withDefaults()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, 0, err
	}
	if _, err := safeio.ExistingDir(absRoot); err != nil {
		return nil, 0, 0, fmt.Errorf("redact: %w", err)
	}
	err = filepath.WalkDir(absRoot, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mode := entry.Type()
		if mode&fs.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() {
			if mode&(fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
				return nil
			}
			// SF3: репозиторий-относительный путь для include/exclude —
			// от policy.RepoRoot, если задан (иначе от корня сканирования).
			rel, relErr := PolicyRel(policy.RepoRoot, absRoot, p)
			if relErr != nil {
				return relErr
			}
			if !policy.Applies(rel) {
				return nil
			}
			findings, scanErr := ScanFile(p, policy.MaxBytes)
			if scanErr != nil {
				return fmt.Errorf("redact scan %s: %w", p, scanErr)
			}
			if len(findings) > 0 {
				found = append(found, FileResult{Path: rel, Findings: findings})
			}
			scanned++
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return found, scanned, total, err
}

// policyRel — путь файла для glob-матчинга: от RepoRoot, если задан и файл
// лежит в нём; иначе от корня сканирования. Файлы вне RepoRoot не матчатся
// include/exclude репозиторий-глобами (вне протокола — пропускаем).
func PolicyRel(repoRoot, scanRoot, full string) (string, error) {
	if repoRoot != "" {
		absRepo, err := filepath.Abs(repoRoot)
		if err != nil {
			return "", err
		}
		if rel, err := relFromRoot(absRepo, full); err == nil {
			return rel, nil
		}
	}
	return relFromRoot(scanRoot, full)
}

// Verify — обязательный блокер (P1-6): при fail-closed политике любая находка
// приводит к ошибке. Report всегда детерминирован и машиночитаем.
func Verify(root string, policy Policy) (*Report, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	found, scanned, total, err := ScanDir(root, policy)
	if err != nil {
		return nil, err
	}
	report := buildReport(found, scanned, total)
	// SF3: guard «included-файлов 0» — include-политика не должна молча
	// выключать блокер (ноль сканированных файлов при непустом include =
	// либо неверный glob, либо политика завела нас в пустоту).
	if len(policy.Include) > 0 && scanned == 0 {
		return report, fmt.Errorf("redaction: include-глобы %q не совпали ни с одним файлом — скан пуст, блокер не может быть подтверждён", policy.Include)
	}
	if len(report.Violations) > 0 && policy.FailOnSecrets {
		return report, fmt.Errorf("redaction: %s", report.Verdict)
	}
	return report, nil
}

func buildReport(found []FileResult, scanned int, total int64) *Report {
	violations := make([]Violation, 0, len(found))
	totalFindings := 0
	for _, f := range found {
		violations = append(violations, Violation{Path: f.Path, Findings: f.Findings})
		totalFindings += len(f.Findings)
	}
	verdict := "clean"
	if totalFindings > 0 {
		verdict = fmt.Sprintf("%d violations в %d файлах", totalFindings, len(violations))
	}
	return &Report{Verdict: verdict, Files: scanned, Bytes: total, Violations: violations}
}

// RedactFile возвращает копию data, где каждая находка заменена на
// [REDACTED:<reason>]. Изменения детерминированы (replace all по правилам).
func RedactFile(data []byte) []byte {
	return redactByLines(splitLines(data))
}

// splitLines разбивает data на строки, сохраняя trailing '\n'.
func splitLines(data []byte) []string {
	lines := strings.SplitAfter(string(data), "\n")
	if last := lines[len(lines)-1]; last == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// redactByLines применяет правила построчно и возвращает результат. Для
// правил с value-группой (secret assignment) применяется тот же фильтр
// likelySecretValue, что и в Scan — иначе scan и redact расходились бы.
//
// Private key обрабатывается как МНОГОСТРОЧНЫЙ блок: тело (base64) между
// BEGIN и END PRIVATE KEY тоже вырезается — иначе в redacted-копии оставался
// бы ключ и создавал false sense of security.
func redactByLines(lines []string) []byte {
	var builder strings.Builder
	inKeyBody := false
	for _, line := range lines {
		cleaned := line
		if inKeyBody {
			cleaned = "[REDACTED:private key]"
			if endPrivateKeyRe().MatchString(line) {
				inKeyBody = false
			}
			builder.WriteString(cleaned)
			continue
		}
		if beginPrivateKeyRe().MatchString(line) {
			cleaned = "[REDACTED:private key]"
			inKeyBody = !endPrivateKeyRe().MatchString(line)
			builder.WriteString(cleaned)
			continue
		}
		for _, rule := range compileSecretRules() {
			if rule.name == "private key" {
				continue // обрабатывается как блок выше
			}
			if rule.valueGroup >= 0 {
				cleaned = rule.regex.ReplaceAllStringFunc(cleaned, func(m string) string {
					loc := rule.regex.FindStringSubmatchIndex(m)
					if loc == nil || len(loc) < rule.valueGroup+2 {
						return m
					}
					value := m[loc[rule.valueGroup]:loc[rule.valueGroup+1]]
					if !likelySecretValue(value) {
						return m
					}
					return "[REDACTED:" + rule.name + "]"
				})
				continue
			}
			cleaned = rule.regex.ReplaceAllString(cleaned, "[REDACTED:"+rule.name+"]")
		}
		builder.WriteString(cleaned)
	}
	return []byte(builder.String())
}

// relPath проверяет, что full лежит внутри root, и возвращает относительный
// путь с '/' разделителями.
func relPath(root, full string) (string, error) {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %s вне root %s", full, root)
	}
	return filepath.ToSlash(rel), nil
}

// relFromRoot нормализует путь относительно корня в slash-separated вид.
func relFromRoot(root, full string) (string, error) {
	rel, err := relPath(root, full)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(path.Clean(rel), "./"), nil
}
