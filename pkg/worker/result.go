package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResultPrefix маркирует единственную машиночитаемую строку результата,
// которую `ai-team worker` печатает в stdout перед завершением.
const ResultPrefix = "ai-team-worker-result: "

// ResultSchemaVersion — версия контракта строки результата.
const ResultSchemaVersion = 1

// Исходы воркер-задачи. Отличают business-исходы run (durable состояние,
// требующее человека) от инфраструктурных сбоев самого воркера.
const (
	OutcomeCompleted       = "completed"
	OutcomeFailed          = "failed"
	OutcomeBlocked         = "blocked"
	OutcomeStopped         = "stopped"
	OutcomeWaitingApproval = "waiting_for_approval"
	OutcomeCanceled        = "canceled"
	OutcomeInfraFailed     = "infra_failed"
)

// Controlled исходы означают, что job дошёл до durable состояния и не
// должен перезапускаться автоматически после истечения lease.
var controlledOutcomes = map[string]bool{
	OutcomeCompleted: true, OutcomeWaitingApproval: true,
	OutcomeBlocked: true, OutcomeStopped: true, OutcomeCanceled: true,
}

// Controlled сообщает, что исход не требует повторного исполнения job.
func (r Result) Controlled() bool {
	return controlledOutcomes[r.Outcome]
}

type Result struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Outcome       string `json:"outcome"`
	Error         string `json:"error,omitempty"`
}

// ParseResult извлекает последнюю строку результата из объединённого
// вывода воркера. Отсутствие валидной строки означает инфраструктурный
// сбой до записи результата (crash, preflight fatal, чужой binary).
func ParseResult(output string) (Result, error) {
	var parsed Result
	last := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, ResultPrefix) {
			last = strings.TrimPrefix(line, ResultPrefix)
		}
	}
	if last == "" {
		return parsed, fmt.Errorf("worker result line не найдена")
	}
	decoder := json.NewDecoder(strings.NewReader(last))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return Result{}, fmt.Errorf("worker result line: %w", err)
	}
	if parsed.SchemaVersion != ResultSchemaVersion || parsed.Outcome == "" {
		return Result{}, fmt.Errorf("worker result line: недопустимый результат")
	}
	return parsed, nil
}
