package pipeline

import (
	"errors"
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// ErrUserStopped — пользователь остановил пайплайн на gate/confirm (exit-код 3).
var ErrUserStopped = errors.New("пайплайн остановлен пользователем")

type RunError struct {
	Outcome workflow.RunOutcome
	Err     error
}

func (e *RunError) Error() string { return e.Err.Error() }

func (e *RunError) Unwrap() error { return e.Err }

// ApprovalRequiredError — required checkpoint не был явно подтверждён.
// Для CLI это управляемая остановка (exit-код 3), а не ошибка агента.
type ApprovalRequiredError struct {
	Checkpoint string
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("%s: требуется явное подтверждение", e.Checkpoint)
}

func (e *ApprovalRequiredError) Unwrap() error { return ErrUserStopped }

// BlockedError — агент сигнализировал BLOCKED (exit-код 2).
type BlockedError struct {
	Agent  string
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("агент %s заблокирован: %s", e.Agent, e.Reason)
}

// NegativeVerdictError — негативный вердикт остановил пайплайн (exit-код 1).
type NegativeVerdictError struct {
	Agent   string
	Verdict verdict.Verdict
}

func (e *NegativeVerdictError) Error() string {
	return fmt.Sprintf("негативный вердикт %s от %s", e.Verdict, e.Agent)
}
