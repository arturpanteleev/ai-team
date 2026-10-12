// Package tokenbudget считает расход токенов по долговечным attempt-манифестам
// и решает, исчерпан ли бюджет задачи или суток. Источник истины — evidence на
// диске, а не память процесса: после рестарта счётчик восстанавливается тем же
// пересчётом и не сбрасывается.
package tokenbudget

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Области бюджета. Значения попадают в событие budget_exceeded.
const (
	ScopeTask  = "task"
	ScopeDaily = "daily"
)

// EventBudgetExceeded — тип события остановки по бюджету.
const EventBudgetExceeded = "budget_exceeded"

// maxManifestBytes ограничивает чтение одного attempt-манифеста.
const maxManifestBytes = 1 << 20

// Usage — суммарный расход токенов. Unknown означает, что хотя бы одна
// модельная попытка не сообщила подтверждённый расход: такую сумму нельзя
// считать нижней границей и сравнивать с лимитом как точную.
type Usage struct {
	Tokens  int64
	Unknown bool
}

// Add складывает расход, превращая переполнение в Unknown.
func (u Usage) Add(other Usage) Usage {
	result := Usage{Unknown: u.Unknown || other.Unknown}
	if other.Tokens < 0 || u.Tokens > math.MaxInt64-other.Tokens {
		result.Unknown = true
		result.Tokens = u.Tokens
		return result
	}
	result.Tokens = u.Tokens + other.Tokens
	return result
}

// AttemptUsage переводит запись попытки в расход. nil — попытка без
// модельного вызова (доставка, исторический формат); не подтверждённая
// запись — неизвестный расход.
func AttemptUsage(usage *workflow.AttemptUsage) Usage {
	if usage == nil {
		return Usage{}
	}
	if !usage.Attested || usage.TokensInput < 0 || usage.TokensOutput < 0 ||
		usage.TokensInput > math.MaxInt64-usage.TokensOutput {
		return Usage{Unknown: true}
	}
	return Usage{Tokens: usage.TokensInput + usage.TokensOutput}
}

// Exceeded описывает исчерпанный бюджет.
type Exceeded struct {
	Scope   string
	Used    int64
	Limit   int64
	Unknown bool
}

func (e *Exceeded) Error() string {
	if e.Unknown {
		return fmt.Sprintf("бюджет токенов (%s) не проверяем: расход неизвестен, лимит %d", e.Scope, e.Limit)
	}
	return fmt.Sprintf("бюджет токенов (%s) исчерпан: израсходовано %d при лимите %d", e.Scope, e.Used, e.Limit)
}

// Check решает, можно ли начинать следующую модельную попытку. Лимит 0 —
// бюджет не задан. Ровно на лимите бюджет исчерпан: следующая попытка
// гарантированно его превысит. Неизвестный расход при заданном лимите —
// остановка (fail-closed).
func Check(scope string, used Usage, limit int64) *Exceeded {
	if limit <= 0 {
		return nil
	}
	if used.Unknown || used.Tokens >= limit {
		return &Exceeded{Scope: scope, Used: used.Tokens, Limit: limit, Unknown: used.Unknown}
	}
	return nil
}

// DayWindow возвращает сутки UTC, которым принадлежит момент t: [start, end).
// Граница суток фиксирована по UTC, чтобы счётчик не зависел от пояса хоста.
func DayWindow(t time.Time) (time.Time, time.Time) {
	utc := t.UTC()
	start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.Add(24 * time.Hour)
}

// Day — метка суток UTC (YYYY-MM-DD).
func Day(t time.Time) string {
	start, _ := DayWindow(t)
	return start.Format("2006-01-02")
}

// Ledger отдаёт расход токенов за полуинтервал времени по всем задачам.
type Ledger interface {
	TokensBetween(start, end time.Time) (Usage, error)
}

// FilesystemLedger пересчитывает расход по attempt-манифестам в
// <target>/.ai-team/runs/<run>/attempts/<attempt>/manifest.json. Попытка
// относится к суткам своего завершения; незавершённая — к суткам начала.
type FilesystemLedger struct {
	RunsRoot string
}

// TokensBetween суммирует расход попыток, относящихся к [start, end).
// Отсутствующий корень — ноль: на инстансе ещё не было задач.
func (l FilesystemLedger) TokensBetween(start, end time.Time) (Usage, error) {
	if l.RunsRoot == "" {
		return Usage{}, errors.New("token ledger: runs root не задан")
	}
	runs, err := os.ReadDir(l.RunsRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("token ledger: %w", err)
	}
	var total Usage
	for _, run := range runs {
		if !run.IsDir() || evidence.ValidateRunID(run.Name()) != nil {
			continue
		}
		attemptsDir := filepath.Join(l.RunsRoot, run.Name(), "attempts")
		attempts, err := os.ReadDir(attemptsDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Usage{}, fmt.Errorf("token ledger: run %s: %w", run.Name(), err)
		}
		for _, attempt := range attempts {
			if !attempt.IsDir() {
				continue
			}
			usage, err := manifestUsage(filepath.Join(attemptsDir, attempt.Name(), "manifest.json"), start, end)
			if err != nil {
				return Usage{}, fmt.Errorf("token ledger: run %s attempt %s: %w", run.Name(), attempt.Name(), err)
			}
			total = total.Add(usage)
		}
	}
	return total, nil
}

func manifestUsage(path string, start, end time.Time) (Usage, error) {
	data, err := safeio.ReadRegularFile(path, maxManifestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		// Каталог попытки без манифеста — попытка ещё не опубликована; её
		// расход учтётся, когда манифест появится.
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, err
	}
	var manifest struct {
		StartedAt  time.Time              `json:"started_at"`
		FinishedAt time.Time              `json:"finished_at"`
		Usage      *workflow.AttemptUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		// Повреждённый манифест нельзя считать нулевым расходом.
		return Usage{Unknown: true}, nil
	}
	at := manifest.FinishedAt
	if at.IsZero() {
		at = manifest.StartedAt
	}
	if at.IsZero() {
		return Usage{Unknown: true}, nil
	}
	if at.Before(start) || !at.Before(end) {
		return Usage{}, nil
	}
	return AttemptUsage(manifest.Usage), nil
}
