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
	"strings"
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

// EventBudgetAllowanceGranted — явная выдача задаче нового бюджета человеком
// после остановки по бюджету задачи. Только это событие сдвигает точку
// отсчёта бюджета задачи; resume без него новый бюджет не выдаёт.
const EventBudgetAllowanceGranted = "budget_allowance_granted"

// maxManifestBytes ограничивает чтение одного attempt-манифеста.
const maxManifestBytes = 1 << 20

// CheckerUsageDir — каталог внутри run, где лежат записи расхода
// агентов-проверяющих. У проверяющего нет собственного attempt-манифеста, а
// модель он вызывает: без этой записи его токены шли бы мимо бюджетов.
const CheckerUsageDir = "checker-usage"

// CheckerUsageRecord — долговечная запись расхода одного запуска проверяющего.
// Поля совпадают с attempt-манифестом, чтобы ledger читал их одинаково.
type CheckerUsageRecord struct {
	RunID      string                 `json:"run_id"`
	Stage      string                 `json:"stage"`
	Checker    string                 `json:"checker"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt time.Time              `json:"finished_at"`
	Usage      *workflow.AttemptUsage `json:"usage"`
}

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
		runDir := filepath.Join(l.RunsRoot, run.Name())
		attemptsDir := filepath.Join(runDir, "attempts")
		attempts, err := os.ReadDir(attemptsDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
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
		checkers, err := checkerUsageBetween(runDir, start, end)
		if err != nil {
			return Usage{}, fmt.Errorf("token ledger: run %s: %w", run.Name(), err)
		}
		total = total.Add(checkers)
	}
	return total, nil
}

// RunCheckerUsage суммирует весь расход проверяющих одного run. Маркер
// начала без итоговой записи — прерванный запуск: его расход неизвестен.
func RunCheckerUsage(runDir string) (Usage, error) {
	total, err := checkerUsageBetween(runDir, time.Time{}, time.Unix(1<<62, 0))
	if err != nil {
		return Usage{}, err
	}
	orphans, err := orphanCheckerMarkers(runDir)
	if err != nil {
		return Usage{}, err
	}
	if len(orphans) > 0 {
		total.Unknown = true
	}
	return total, nil
}

// checkerUsageBetween суммирует итоговые записи. Маркеры начала дневной итог
// не видит: запуск, который идёт прямо сейчас, учитывается по завершении —
// как и попытка агента этапа до публикации манифеста.
func checkerUsageBetween(runDir string, start, end time.Time) (Usage, error) {
	dir := filepath.Join(runDir, CheckerUsageDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, err
	}
	var total Usage
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		usage, err := manifestUsage(filepath.Join(dir, entry.Name()), start, end)
		if err != nil {
			return Usage{}, fmt.Errorf("checker usage %s: %w", entry.Name(), err)
		}
		total = total.Add(usage)
	}
	return total, nil
}

const checkerStartedExt = ".started"

func orphanCheckerMarkers(runDir string) ([]string, error) {
	dir := filepath.Join(runDir, CheckerUsageDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var orphans []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != checkerStartedExt {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), checkerStartedExt)
		if _, err := os.Lstat(filepath.Join(dir, key+".json")); errors.Is(err, fs.ErrNotExist) {
			orphans = append(orphans, key)
		} else if err != nil {
			return nil, err
		}
	}
	return orphans, nil
}

// BeginCheckerUsage атомарно пишет маркер начала запуска проверяющего до
// вызова модели и возвращает ключ записи. Ключ — префикс (попытка этапа,
// проверяющий, раунд) и первый свободный номер: часы в ключ не входят.
func BeginCheckerUsage(runDir, prefix string, record CheckerUsageRecord) (string, error) {
	dir, err := safeio.EnsureDir(runDir, CheckerUsageDir)
	if err != nil {
		return "", err
	}
	if prefix == "" || filepath.Base(prefix) != prefix || strings.ContainsAny(prefix, "\x00") {
		return "", fmt.Errorf("checker usage key %q is unsafe", prefix)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	for index := 1; index < 10000; index++ {
		key := fmt.Sprintf("%s-%03d", prefix, index)
		if _, err := os.Lstat(filepath.Join(dir, key+".json")); err == nil {
			continue
		}
		err := createFileAtomic(dir, key+checkerStartedExt, data)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return key, err
	}
	return "", fmt.Errorf("checker usage key %q exhausted", prefix)
}

// FinishCheckerUsage атомарно пишет итоговую запись расхода под ключом.
func FinishCheckerUsage(runDir, key string, record CheckerUsageRecord) error {
	if key == "" || filepath.Base(key) != key {
		return fmt.Errorf("checker usage key %q is unsafe", key)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	dir, err := safeio.EnsureDir(runDir, CheckerUsageDir)
	if err != nil {
		return err
	}
	return createFileAtomic(dir, key+".json", data)
}

// ReconcileCheckerUsage превращает маркеры прерванных запусков в итоговые
// записи с неизвестным расходом: после рестарта их видят и бюджет задачи, и
// дневной итог. Вызывается только владельцем run (под workspace lock), когда
// ни один проверяющий этого run не исполняется.
func ReconcileCheckerUsage(runDir string) (int, error) {
	orphans, err := orphanCheckerMarkers(runDir)
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(runDir, CheckerUsageDir)
	for _, key := range orphans {
		data, err := safeio.ReadRegularFile(filepath.Join(dir, key+checkerStartedExt), maxManifestBytes)
		if err != nil {
			return 0, err
		}
		var record CheckerUsageRecord
		if err := json.Unmarshal(data, &record); err != nil || record.StartedAt.IsZero() {
			// Нечитаемый маркер относится к суткам своего создания, а не к
			// суткам resume.
			info, statErr := os.Lstat(filepath.Join(dir, key+checkerStartedExt))
			if statErr != nil {
				return 0, statErr
			}
			record = CheckerUsageRecord{StartedAt: info.ModTime().UTC()}
		}
		record.FinishedAt = record.StartedAt
		record.Usage = &workflow.AttemptUsage{}
		if err := FinishCheckerUsage(runDir, key, record); err != nil && !errors.Is(err, fs.ErrExist) {
			return 0, err
		}
	}
	return len(orphans), nil
}

// WriteCheckerUsage сохраняет итоговую запись без отдельного маркера.
func WriteCheckerUsage(runDir string, record CheckerUsageRecord) error {
	key, err := BeginCheckerUsage(runDir, record.Stage+"-"+record.Checker, record)
	if err != nil {
		return err
	}
	return FinishCheckerUsage(runDir, key, record)
}

// createFileAtomic пишет файл через временный файл, fsync и link: читатель
// видит либо полный JSON, либо ничего, а существующий файл не перезаписывается.
func createFileAtomic(dir, name string, data []byte) error {
	temp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	// Права как у остальных evidence-файлов run (0644), а не 0600 CreateTemp.
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, filepath.Join(dir, name)); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	if closeErr := directory.Close(); syncErr == nil {
		syncErr = closeErr
	}
	return syncErr
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
