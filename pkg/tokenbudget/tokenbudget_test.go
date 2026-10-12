package tokenbudget

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestCheckBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		used    Usage
		limit   int64
		blocked bool
	}{
		{"limit not configured", Usage{Tokens: 1 << 40}, 0, false},
		{"negative limit is not configured", Usage{Tokens: 10}, -1, false},
		{"one below limit", Usage{Tokens: 99}, 100, false},
		{"exactly at limit", Usage{Tokens: 100}, 100, true},
		{"one above limit", Usage{Tokens: 101}, 100, true},
		{"unknown usage fails closed", Usage{Unknown: true}, 100, true},
		{"unknown usage without limit", Usage{Unknown: true}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(ScopeTask, tc.used, tc.limit)
			if (got != nil) != tc.blocked {
				t.Fatalf("Check(%+v, %d) = %+v, blocked want %t", tc.used, tc.limit, got, tc.blocked)
			}
			if got != nil && (got.Scope != ScopeTask || got.Limit != tc.limit || got.Used != tc.used.Tokens || got.Unknown != tc.used.Unknown || got.Error() == "") {
				t.Fatalf("exceeded details: %+v", got)
			}
		})
	}
}

func TestUsageAddAndAttemptUsage(t *testing.T) {
	if sum := (Usage{Tokens: 2}).Add(Usage{Tokens: 3}); sum.Tokens != 5 || sum.Unknown {
		t.Fatalf("sum = %+v", sum)
	}
	if sum := (Usage{Tokens: math.MaxInt64}).Add(Usage{Tokens: 1}); !sum.Unknown {
		t.Fatalf("overflow must become unknown: %+v", sum)
	}
	if sum := (Usage{}).Add(Usage{Tokens: -1}); !sum.Unknown {
		t.Fatalf("negative tokens must become unknown: %+v", sum)
	}
	cases := []struct {
		name  string
		usage *workflow.AttemptUsage
		want  Usage
	}{
		{"no model call", nil, Usage{}},
		{"attested", &workflow.AttemptUsage{Attested: true, TokensInput: 7, TokensOutput: 3}, Usage{Tokens: 10}},
		{"not attested", &workflow.AttemptUsage{TokensInput: 7}, Usage{Unknown: true}},
		{"negative", &workflow.AttemptUsage{Attested: true, TokensInput: -1}, Usage{Unknown: true}},
		{"overflow", &workflow.AttemptUsage{Attested: true, TokensInput: math.MaxInt64, TokensOutput: 1}, Usage{Unknown: true}},
	}
	for _, tc := range cases {
		if got := AttemptUsage(tc.usage); got != tc.want {
			t.Fatalf("%s: AttemptUsage = %+v want %+v", tc.name, got, tc.want)
		}
	}
}

// Граница суток фиксирована по UTC: момент в другом поясе относится к суткам
// своего UTC-представления, а не к локальной дате.
func TestDayWindowUsesUTC(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	cases := []struct {
		name string
		at   time.Time
		day  string
	}{
		{"start of UTC day", time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), "2026-10-12"},
		{"last nanosecond of UTC day", time.Date(2026, 10, 12, 23, 59, 59, 999999999, time.UTC), "2026-10-12"},
		{"local next day is still previous UTC day", time.Date(2026, 10, 13, 2, 30, 0, 0, moscow), "2026-10-12"},
		{"local midnight plus three hours is the UTC day", time.Date(2026, 10, 13, 3, 0, 0, 0, moscow), "2026-10-13"},
	}
	for _, tc := range cases {
		start, end := DayWindow(tc.at)
		if got := Day(tc.at); got != tc.day {
			t.Fatalf("%s: Day = %s want %s", tc.name, got, tc.day)
		}
		if start.Location() != time.UTC || end.Sub(start) != 24*time.Hour || tc.at.Before(start) || !tc.at.Before(end) {
			t.Fatalf("%s: window [%s, %s) does not contain %s", tc.name, start, end, tc.at)
		}
	}
}

func writeManifest(t *testing.T, root, runID, attemptID string, value any) {
	t.Helper()
	dir := filepath.Join(root, runID, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, ok := value.([]byte)
	if !ok {
		var err error
		data, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type manifestFixture struct {
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt time.Time              `json:"finished_at"`
	Usage      *workflow.AttemptUsage `json:"usage,omitempty"`
}

func TestFilesystemLedgerCountsAttemptsOfUTCDay(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	attested := func(tokens int64) *workflow.AttemptUsage {
		return &workflow.AttemptUsage{Attested: true, TokensInput: tokens, TokensOutput: 1}
	}
	runA, runB := "20261011T230000Z-aaaaaaaa", "20261012T100000Z-bbbbbbbb"
	// Задача создана вчера, но попытка завершилась сегодня — считается сегодня.
	writeManifest(t, root, runA, "a-1", manifestFixture{StartedAt: day.Add(-time.Hour), FinishedAt: day, Usage: attested(9)})
	writeManifest(t, root, runA, "a-0", manifestFixture{StartedAt: day.Add(-2 * time.Hour), FinishedAt: day.Add(-time.Nanosecond), Usage: attested(1000)})
	writeManifest(t, root, runB, "b-1", manifestFixture{StartedAt: day.Add(10 * time.Hour), FinishedAt: day.Add(11 * time.Hour), Usage: attested(19)})
	writeManifest(t, root, runB, "b-2", manifestFixture{StartedAt: day.Add(24 * time.Hour), FinishedAt: day.Add(24 * time.Hour), Usage: attested(1000)})
	// Доставка без usage — ноль; незавершённая попытка относится к суткам начала.
	writeManifest(t, root, runB, "b-3", manifestFixture{StartedAt: day.Add(12 * time.Hour), FinishedAt: day.Add(12 * time.Hour)})
	writeManifest(t, root, runB, "b-4", manifestFixture{StartedAt: day.Add(13 * time.Hour), Usage: attested(4)})
	// Каталог без манифеста и чужой каталог игнорируются.
	if err := os.MkdirAll(filepath.Join(root, runB, "attempts", "b-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "not-a-run", "attempts", "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	ledger := FilesystemLedger{RunsRoot: root}
	start, end := DayWindow(day.Add(5 * time.Hour))
	got, err := ledger.TokensBetween(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Usage{Tokens: 10 + 20 + 5}); got != want {
		t.Fatalf("daily usage = %+v want %+v", got, want)
	}
	// Пересчёт новым экземпляром (рестарт процесса) даёт то же значение.
	again, err := FilesystemLedger{RunsRoot: root}.TokensBetween(start, end)
	if err != nil || again != got {
		t.Fatalf("restart recount = %+v err=%v, want %+v", again, err, got)
	}
}

func TestFilesystemLedgerFailsClosedOnUnknownUsage(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	start, end := DayWindow(day)
	cases := []struct {
		name     string
		manifest any
	}{
		{"unattested usage", manifestFixture{StartedAt: day, FinishedAt: day.Add(time.Hour), Usage: &workflow.AttemptUsage{TokensInput: 3}}},
		{"corrupted manifest", []byte("{not json")},
		{"manifest without timestamps", manifestFixture{Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeManifest(t, root, "20261012T010000Z-cccccccc", "c-1", tc.manifest)
			got, err := FilesystemLedger{RunsRoot: root}.TokensBetween(start, end)
			if err != nil || !got.Unknown {
				t.Fatalf("usage = %+v err=%v, want unknown", got, err)
			}
		})
	}
}

func TestFilesystemLedgerEmptyAndMisconfigured(t *testing.T) {
	start, end := DayWindow(time.Now())
	got, err := FilesystemLedger{RunsRoot: filepath.Join(t.TempDir(), "missing")}.TokensBetween(start, end)
	if err != nil || got != (Usage{}) {
		t.Fatalf("missing runs root = %+v err=%v", got, err)
	}
	if _, err := (FilesystemLedger{}).TokensBetween(start, end); err == nil {
		t.Fatal("empty runs root must be an error")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (FilesystemLedger{RunsRoot: file}).TokensBetween(start, end); err == nil {
		t.Fatal("unreadable runs root must be an error")
	}
}

// Расход проверяющих лежит рядом с манифестами и входит в дневной итог и в
// итог run.
func TestCheckerUsageRecordsCountTowardLedger(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	runDir := filepath.Join(root, "20261012T010000Z-dddddddd")
	writeManifest(t, root, "20261012T010000Z-dddddddd", "d-1", manifestFixture{StartedAt: day, FinishedAt: day.Add(time.Hour), Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 3}})
	records := []CheckerUsageRecord{
		{Stage: "spec", Checker: "spec-checker", StartedAt: day.Add(2 * time.Hour), FinishedAt: day.Add(2 * time.Hour), Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 10, TokensOutput: 2}},
		{Stage: "spec", Checker: "spec-checker", StartedAt: day.Add(25 * time.Hour), FinishedAt: day.Add(25 * time.Hour), Usage: &workflow.AttemptUsage{Attested: true, TokensInput: 100}},
	}
	for _, record := range records {
		if err := WriteCheckerUsage(runDir, record); err != nil {
			t.Fatal(err)
		}
	}
	start, end := DayWindow(day)
	daily, err := FilesystemLedger{RunsRoot: root}.TokensBetween(start, end)
	if err != nil || daily != (Usage{Tokens: 15}) {
		t.Fatalf("daily usage with checker = %+v err=%v", daily, err)
	}
	total, err := RunCheckerUsage(runDir)
	if err != nil || total != (Usage{Tokens: 112}) {
		t.Fatalf("run checker usage = %+v err=%v", total, err)
	}
	if err := WriteCheckerUsage(runDir, CheckerUsageRecord{Stage: "spec", Checker: "x", StartedAt: day.Add(3 * time.Hour), FinishedAt: day.Add(3 * time.Hour), Usage: &workflow.AttemptUsage{}}); err != nil {
		t.Fatal(err)
	}
	if unknown, _ := RunCheckerUsage(runDir); !unknown.Unknown {
		t.Fatal("checker without attested usage must make usage unknown")
	}
	if empty, err := RunCheckerUsage(t.TempDir()); err != nil || empty != (Usage{}) {
		t.Fatalf("run without checker records = %+v err=%v", empty, err)
	}
}

// Маркер начала пишется до вызова модели. Пока итога нет, дневной итог его не
// видит (запуск идёт), а итог run считает расход неизвестным; reconcile после
// рестарта превращает маркер в итоговую запись с неизвестным расходом.
func TestCheckerUsageMarkersAndAtomicKeys(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "20261012T010000Z-eeeeeeee")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	record := CheckerUsageRecord{Stage: "spec", Checker: "spec-checker", StartedAt: at}
	// Фиксированные часы не дают одинаковых ключей.
	first, err := BeginCheckerUsage(runDir, "attempt-1-spec-checker-r01", record)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BeginCheckerUsage(runDir, "attempt-1-spec-checker-r01", record)
	if err != nil || second == first {
		t.Fatalf("keys must be unique under a fixed clock: %q %q err=%v", first, second, err)
	}
	if _, err := BeginCheckerUsage(runDir, "../escape", record); err == nil {
		t.Fatal("unsafe key prefix must be rejected")
	}
	done := record
	done.FinishedAt = at.Add(time.Minute)
	done.Usage = &workflow.AttemptUsage{Attested: true, TokensInput: 5}
	if err := FinishCheckerUsage(runDir, first, done); err != nil {
		t.Fatal(err)
	}
	if err := FinishCheckerUsage(runDir, first, done); err == nil {
		t.Fatal("final record must not be overwritten")
	}
	start, end := DayWindow(at)
	daily, err := FilesystemLedger{RunsRoot: root}.TokensBetween(start, end)
	if err != nil || daily != (Usage{Tokens: 5}) {
		t.Fatalf("in-flight marker must not change the daily total: %+v err=%v", daily, err)
	}
	run, err := RunCheckerUsage(runDir)
	if err != nil || !run.Unknown || run.Tokens != 5 {
		t.Fatalf("orphan marker must make run usage unknown: %+v err=%v", run, err)
	}
	reconciled, err := ReconcileCheckerUsage(runDir)
	if err != nil || reconciled != 1 {
		t.Fatalf("reconcile = %d err=%v", reconciled, err)
	}
	if again, err := ReconcileCheckerUsage(runDir); err != nil || again != 0 {
		t.Fatalf("reconcile must be idempotent: %d err=%v", again, err)
	}
	daily, err = FilesystemLedger{RunsRoot: root}.TokensBetween(start, end)
	if err != nil || !daily.Unknown {
		t.Fatalf("reconciled interrupted checker must be unknown in the daily total: %+v err=%v", daily, err)
	}
	entries, err := os.ReadDir(filepath.Join(runDir, CheckerUsageDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" && filepath.Ext(entry.Name()) != ".started" {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
