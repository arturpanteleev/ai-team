// Package stagecheck выполняет декларативные проверки результата этапа после
// завершения попытки и формирует отдельный человекочитаемый отчёт.
//
// B-40 вводит два вида проверок из §7 спецификации:
//
//   - hard: детерминированные правила над результатом
//     (required_sections, verdict_marker, pr_exists/pr_open/pr_base_branch);
//   - agent: прогон агента-проверяющего, который выдаёт md-отчёт с
//     маркером `Verdict: PASSED | FAILED`.
//
// Пакет не знает о пайплайне: hard-правило прогоняется над уже прочитанным
// содержимым, а прогон агента выполняет вызывающая сторона и передаёт отчёт
// сюда только для разбора вердикта. Это позволяет тестировать ядро проверок
// без runtime и без сети.
package stagecheck

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

// Kind — вид проверки. Совпадает со значением config.TemplateCheck.Kind.
const (
	KindHard  = "hard"
	KindAgent = "agent"
)

// Status — итог проверки этапа.
const (
	StatusPassed = "passed"
	StatusFailed = "failed"
	StatusError  = "error"
)

// RuleStatus — итог отдельного hard-правила. Значения совместимы со
// статусами delivery.PullRequestRuleResult.
const (
	RulePassed  = "passed"
	RuleFailed  = "failed"
	RuleSkipped = "skipped"
)

// RuleResult — результат одного правила.
type RuleResult struct {
	Rule   string `json:"rule"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Report — результат проверки одного этапа.
type Report struct {
	Stage   string       `json:"stage"`
	Kind    string       `json:"kind"`
	Status  string       `json:"status"`
	Verdict string       `json:"verdict,omitempty"`
	Rules   []RuleResult `json:"rules,omitempty"`
	Notes   string       `json:"notes,omitempty"`
}

// PRCheckFunc прогоняет pr_* правила над ссылкой на pull request. Реализация
// делегирует в delivery.CheckPullRequest (B-36), чтобы не дублировать семантику
// gh pr view и не расходиться с delivery-предусловиями.
type PRCheckFunc func(ctx context.Context, reference, expectedBaseBranch string) ([]RuleResult, error)

// HardInput — вход детерминированной проверки.
type HardInput struct {
	Stage              string
	ResultType         string
	Content            string
	Reference          string
	ExpectedBaseBranch string
	RequiredSections   []string
	Rules              []string
	PRCheck            PRCheckFunc
}

// RunHard выполняет hard-правила и возвращает отчёт. Ошибки исполнения
// отдельного правила не бросаются наружу: они попадают в RuleResult со
// статусом "error", а итоговый статус становится "error", чтобы вызывающая
// сторона зафиксировала check_error, а не молчаливый проход.
func RunHard(ctx context.Context, in HardInput) Report {
	report := Report{Stage: in.Stage, Kind: KindHard}

	for _, section := range in.RequiredSections {
		if in.ResultType != "md" {
			report.Rules = append(report.Rules, RuleResult{
				Rule: "required_sections", Status: RuleSkipped,
				Reason: "правило применимо только к result md",
			})
			continue
		}
		if hasHeading(in.Content, section) {
			report.Rules = append(report.Rules, RuleResult{Rule: "required_sections:" + section, Status: RulePassed})
			continue
		}
		report.Rules = append(report.Rules, RuleResult{
			Rule: "required_sections:" + section, Status: RuleFailed,
			Reason: fmt.Sprintf("заголовок %q не найден", section),
		})
	}

	prRequested := false
	for _, rule := range in.Rules {
		switch rule {
		case "required_sections":
			// required_sections описывается отдельными полями stage, а не
			// строкой в rules; без них правило нечего проверять.
			if len(in.RequiredSections) == 0 {
				report.Rules = append(report.Rules, RuleResult{
					Rule: "required_sections", Status: RuleSkipped,
					Reason: "required_sections не заданы",
				})
			}
		case "verdict_marker":
			_, ok := ParseVerdict(in.Content)
			if verdict.Parse(in.Content) != verdict.None || ok {
				report.Rules = append(report.Rules, RuleResult{Rule: "verdict_marker", Status: RulePassed})
			} else {
				report.Rules = append(report.Rules, RuleResult{
					Rule: "verdict_marker", Status: RuleFailed,
					Reason: "маркер вердикта в результате не найден",
				})
			}
		case "pr_exists", "pr_open", "pr_base_branch":
			prRequested = true
		default:
			report.Rules = append(report.Rules, RuleResult{
				Rule: rule, Status: "error",
				Reason: "правило не реализовано",
			})
		}
	}

	if prRequested {
		report.Rules = append(report.Rules, runPRRules(ctx, in)...)
	}

	report.Status = aggregate(report.Rules)
	return report
}

func runPRRules(ctx context.Context, in HardInput) []RuleResult {
	if strings.TrimSpace(in.Reference) == "" {
		return []RuleResult{{
			Rule: "pr_exists", Status: RuleFailed,
			Reason: "результат этапа не содержит ссылки на pull request",
		}}
	}
	if in.PRCheck == nil {
		return []RuleResult{{
			Rule: "pr_exists", Status: "error",
			Reason: "проверка pull request недоступна",
		}}
	}
	results, err := in.PRCheck(ctx, in.Reference, in.ExpectedBaseBranch)
	if err != nil {
		return []RuleResult{{
			Rule: "pr_exists", Status: "error",
			Reason: err.Error(),
		}}
	}
	var filtered []RuleResult
	for _, result := range results {
		switch result.Rule {
		case "pr_exists", "pr_open", "pr_base_branch":
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// RunAgent разбирает отчёт агента-проверяющего. Принимаются как канонические
// B-40-маркеры `Verdict: PASSED | FAILED`, так и уже существующие контракты
// `**Verdict:** APPROVED | CHANGES_REQUESTED | REJECTED` и `Result: PASS | FAIL`
// (pkg/verdict), чтобы встроенный профиль не ломался до появления выделенных
// агентов-проверяющих (B-43).
func RunAgent(stage, source, report string) Report {
	value, ok := ParseVerdict(report)
	result := Report{Stage: stage, Kind: KindAgent, Verdict: value}
	if !ok {
		result.Status = StatusError
		result.Notes = "в отчёте агента-проверяющего не найден вердикт PASSED|FAILED"
		return result
	}
	if value == "PASSED" {
		result.Status = StatusPassed
		return result
	}
	result.Status = StatusFailed
	if source != "" {
		result.Notes = "агент-проверяющий " + source + " вынес вердикт " + value
	}
	return result
}

var (
	passedMarker = regexp.MustCompile(`(?im)^\s*\**(?:Verdict|Result):\**\s+(PASSED|PASS|APPROVED)\s*$`)
	failedMarker = regexp.MustCompile(`(?im)^\s*\**(?:Verdict|Result):\**\s+(FAILED|FAIL|CHANGES_REQUESTED|REJECTED)\s*$`)
)

// ParseVerdict возвращает нормализованный вердикт PASSED/FAILED из отчёта
// агента-проверяющего. Маркеры внутри data-регионов (fenced code, blockquote)
// не читаются как контроль.
func ParseVerdict(report string) (string, bool) {
	content := verdict.StripDataRegions(report)
	if passedMarker.MatchString(content) {
		return "PASSED", true
	}
	if failedMarker.MatchString(content) {
		return "FAILED", true
	}
	return "", false
}

func aggregate(rules []RuleResult) string {
	status := StatusPassed
	for _, rule := range rules {
		switch rule.Status {
		case "error":
			return StatusError
		case RuleFailed:
			status = StatusFailed
		}
	}
	return status
}

func hasHeading(content, section string) bool {
	target := normalizeHeading(section)
	if target == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if normalizeHeading(strings.TrimLeft(trimmed, "#")) == target {
			return true
		}
	}
	return false
}

func normalizeHeading(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, ":")
	return strings.ToLower(strings.TrimSpace(value))
}

// Markdown рендерит отчёт для сохранения и показа в задаче.
func (r Report) Markdown() []byte {
	var builder strings.Builder
	builder.WriteString("# Проверка этапа ")
	builder.WriteString(r.Stage)
	builder.WriteString("\n\n")
	builder.WriteString("- Вид проверки: ")
	builder.WriteString(r.Kind)
	builder.WriteString("\n")
	builder.WriteString("- Итог: ")
	builder.WriteString(r.Status)
	builder.WriteString("\n")
	if r.Verdict != "" {
		builder.WriteString("- Вердикт: ")
		builder.WriteString(r.Verdict)
		builder.WriteString("\n")
	}
	if len(r.Rules) > 0 {
		builder.WriteString("\n## Правила\n\n")
		for _, rule := range r.Rules {
			builder.WriteString("- ")
			builder.WriteString(rule.Rule)
			builder.WriteString(": ")
			builder.WriteString(rule.Status)
			if rule.Reason != "" {
				builder.WriteString(" — ")
				builder.WriteString(rule.Reason)
			}
			builder.WriteString("\n")
		}
	}
	if strings.TrimSpace(r.Notes) != "" {
		builder.WriteString("\n## Замечания\n\n")
		builder.WriteString(strings.TrimSpace(r.Notes))
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}
