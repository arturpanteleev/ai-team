package worker

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

// ResultPrefix маркирует единственную машиночитаемую строку результата,
// которую `ai-team worker` печатает в stdout перед завершением.
const ResultPrefix = "ai-team-worker-result: "

// ResultSchemaVersion — версия контракта строки результата.
const ResultSchemaVersion = 2
const MaxResultBytes = 4 << 10
const MaxResultErrorBytes = 2 << 10

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
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Operation     Operation `json:"operation"`
	Outcome       string    `json:"outcome"`
	Error         string    `json:"error,omitempty"`
}

// ParseResult accepts exactly one bounded result line from combined worker
// output. This is an untrusted status report, never evidence of human approval.
// Missing or malformed output is an infrastructure failure.
func ParseResult(output string) (Result, error) {
	var parsed Result
	last := ""
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, ResultPrefix) {
			last = strings.TrimPrefix(line, ResultPrefix)
			count++
		}
	}
	if last == "" {
		return parsed, fmt.Errorf("worker result line не найдена")
	}
	if count != 1 {
		return parsed, errors.New("worker result line должна быть единственной")
	}
	if len(last) > MaxResultBytes {
		return parsed, errors.New("worker result line превышает лимит размера")
	}
	if err := strictjson.Unmarshal([]byte(last), MaxResultBytes, &parsed); err != nil {
		return Result{}, fmt.Errorf("worker result line: %w", err)
	}
	if parsed.SchemaVersion != ResultSchemaVersion {
		return Result{}, fmt.Errorf("worker result line: неподдерживаемая schema_version %d (ожидается %d)", parsed.SchemaVersion, ResultSchemaVersion)
	}
	if parsed.RunID == "" || parsed.Operation == "" || !validOutcome(parsed.Outcome) ||
		len(parsed.Error) > MaxResultErrorBytes {
		return Result{}, fmt.Errorf("worker result line: недопустимый результат")
	}
	return parsed, nil
}

// ValidateFor binds an untrusted worker result to the exact job that the
// controller launched. A result can report an outcome; it cannot express an
// approval decision or control-plane operation (unknown JSON fields fail closed).
func (r Result) ValidateFor(job Job) error {
	if r.RunID != job.RunID {
		return errors.New("worker result: run_id не совпадает с job")
	}
	if r.Operation != job.Operation {
		return errors.New("worker result: operation не совпадает с job")
	}
	return nil
}

func validOutcome(value string) bool {
	switch value {
	case OutcomeCompleted, OutcomeFailed, OutcomeBlocked, OutcomeStopped,
		OutcomeWaitingApproval, OutcomeCanceled, OutcomeInfraFailed:
		return true
	default:
		return false
	}
}
