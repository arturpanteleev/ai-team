package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/redact"
)

// redactionPolicy собирает policy из redaction-секции конфига (nil-конфиг —
// канонические fail-closed дефолты): include/exclude + disable_export_block.
func redactionPolicy(cfg *config.Config) redact.Policy {
	policy := redact.Policy{FailOnSecrets: true}
	if cfg != nil && cfg.Redaction != nil {
		policy.Include = cfg.Redaction.Include
		policy.Exclude = cfg.Redaction.Exclude
		policy.FailOnSecrets = cfg.Redaction.EffectiveFailExportOnSecrets()
	}
	return policy
}

// loadPolicyConfig грузит .ai-team/config.yaml; при отсутствии файла отдаёт
// пустой конфиг (дефолт-политика), а не ошибку.
func loadPolicyConfig(target string) (*config.Config, error) {
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if _, err := os.Lstat(cfgPath); err != nil {
		if os.IsNotExist(err) {
			return &config.Config{}, nil
		}
		return nil, err
	}
	return config.Load(cfgPath)
}

// resolveScanPath резолвит repository-относительный --path внутри корня
// сканирования и проверяет, что результат не выходит за его пределы
// (контентный guard, аналог validRunID для --run — защита от ../..-обхода).
func resolveScanPath(base, pathArg string) (string, error) {
	joined := filepath.Join(base, strings.TrimPrefix(filepath.FromSlash(pathArg), "./"))
	rel, err := filepath.Rel(base, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("--path %q выходит за пределы корня сканирования", pathArg)
	}
	return joined, nil
}

// cmdRedact — P1-6 privacy-контракт:
//
//	ai-team redact verify --target <dir> [--run <id>]
//	ai-team redact scan   --target <dir> [--path <rel>]
//	ai-team redact redact --target <dir> --out <dir> [--path <rel>]
//
// Подкоманда (verify|scan|redact) — первый позиционный аргумент; её нужно
// вынуть ДО flag.Parse, иначе Go-flag остановится на первом не-флаге и
// задокументированная форма `redact verify --target <dir>` не разберёт флаги.
func cmdRedact() {
	sub := ""
	rest := os.Args[2:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub = rest[0]
		rest = rest[1:]
	}

	redactFlags := flag.NewFlagSet("redact", flag.ExitOnError)
	target := redactFlags.String("target", ".", "Путь к целевому проекту")
	runID := redactFlags.String("run", "", "Ограничить скан одним run (run_id)")
	pathArg := redactFlags.String("path", "", "Ограничить скан repository-относительным путём")
	outDir := redactFlags.String("out", ".ai-team/redacted", "Каталог для redaction-копии (для redact)")
	if err := redactFlags.Parse(rest); err != nil {
		fatal("Ошибка аргументов redact: %v", err)
	}
	if redactFlags.NArg() != 0 {
		fatal("Неожиданные аргументы redact: %s", strings.Join(redactFlags.Args(), " "))
	}

	switch sub {
	case "verify", "scan", "redact":
	default:
		fatal("Использование: ai-team redact < verify | scan | redact > [--target <dir>] [--run <id>] [--path <rel>] [--out <dir>]")
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absolute
	requireControlRoot(*target)

	cfg, err := loadPolicyConfig(*target)
	if err != nil {
		fatal("Ошибка загрузки конфига: %v", err)
	}
	policy := redactionPolicy(cfg)
	// SF3: include/exclude glob'ы документированы repository-relative — матчим
	// их от корня репозитория, а не от суженного корня сканирования.
	policy.RepoRoot = *target

	scanRoot, err := redactRoot(*target)
	if err != nil {
		fatal("%v", err)
	}
	if *runID != "" {
		if !validRunID(*runID) {
			fatal("недопустимый run_id %q", *runID)
		}
		scanRoot = filepath.Join(scanRoot, *runID)
	}
	if *pathArg != "" {
		scanRoot, err = resolveScanPath(scanRoot, *pathArg)
		if err != nil {
			fatal("%v", err)
		}
	}

	// SF4: относительный --out резолвится от target, а не от CWD (match README);
	// default .ai-team/redacted — каталог внутри target, но вне evidence-корня.
	if *outDir == "" {
		fatal("redact: --out обязателен для подкоманды redact")
	}
	if !filepath.IsAbs(*outDir) {
		*outDir = filepath.Join(*target, *outDir)
	}

	switch sub {
	case "verify":
		report, err := redact.Verify(scanRoot, policy)
		if err != nil {
			if report != nil {
				printUnscanned(report.Unscanned)
			}
			logging.Fail(logging.Record{Level: "error", Command: "redact", Type: "redact_verify",
				Message: err.Error(), Data: redactReportData(report)},
				"✗ Redaction %s: %v", scanRoot, err)
			os.Exit(exitFailed)
		}
		// Непросканированное печатается и при успехе: политика могла быть
		// не fail-closed, но слепая зона от этого не перестаёт быть слепой.
		printUnscanned(report.Unscanned)
		fmt.Printf("✓ Redaction %s: clean (%d файлов)\n", scanRoot, report.Files)
		logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_verify",
			Message: "Redaction clean", Data: redactReportData(report), Exit: exitOK})
	case "scan":
		found, scanned, total, err := redact.ScanDir(scanRoot, policy)
		if err != nil {
			fatal("Ошибка скана: %v", err)
		}
		report := redact.Report{Files: scanned, Bytes: total, Verdict: "clean"}
		findings := 0
		for _, f := range found {
			if len(f.Findings) > 0 {
				report.Violations = append(report.Violations, redact.Violation{Path: f.Path, Findings: f.Findings})
				findings += len(f.Findings)
			}
			if len(f.Unscanned) > 0 {
				report.Unscanned = append(report.Unscanned, redact.UnscannedFile{Path: f.Path, Items: f.Unscanned})
			}
		}
		for _, v := range report.Violations {
			for _, f := range v.Findings {
				fmt.Printf("  %s:%d %s (%s)\n", v.Path, f.Line, f.Matched, f.Reason)
			}
		}
		printUnscanned(report.Unscanned)
		logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_scan",
			Message: fmt.Sprintf("Scan: %d files, %d findings, %d unscanned",
				scanned, findings, countUnscanned(report.Unscanned)),
			Data: redactReportData(&report), Exit: exitOK})
	case "redact":
		if err := applyRedact(scanRoot, *outDir, policy); err != nil {
			fatal("redact: %v", err)
		}
	}
}

func applyRedact(source, out string, policy redact.Policy) error {
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("источник не найден: %v", err)
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("source path: %v", err)
	}
	source = absSource
	if !filepath.IsAbs(out) {
		abs, err := filepath.Abs(out)
		if err != nil {
			return fmt.Errorf("out path: %v", err)
		}
		out = abs
	}
	// SF4: запретить out внутри source — иначе WalkDir рекурсивно спиралит по
	// создаваемой redacted-копии (излюбленный путь к бесконечному циклу).
	rel, err := filepath.Rel(source, out)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("--out (%s) не может лежать внутри источника (%s)", out, source)
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("целевой каталог уже существует: %s", out)
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return fmt.Errorf("создание out: %v", err)
	}
	var files, redacted, total int64
	var unscanned []redact.UnscannedFile
	walkErr := filepath.WalkDir(source, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(out, rel), 0755)
		}
		if entry.Type()&fs.ModeSymlink != 0 || entry.Type()&(fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
			return nil
		}
		// SF3: include/exclude — repository-relative (от policy.RepoRoot),
		// а не от суженного корня сканирования.
		matcherRel, matcherErr := redact.PolicyRel(policy.RepoRoot, source, p)
		if matcherErr != nil {
			return matcherErr
		}
		if !policy.Applies(matcherRel) {
			return nil
		}
		result, err := redact.ScanFile(p, policy.MaxBytes)
		if err != nil {
			return err
		}
		// QS-08: файл с непросканированными участками копируется как есть,
		// значит в «санированной» копии может остаться секрет. Молчать об
		// этом нельзя — счётчик уходит в отчёт команды.
		if len(result.Unscanned) > 0 {
			unscanned = append(unscanned, redact.UnscannedFile{Path: rel, Items: result.Unscanned})
		}
		target := filepath.Join(out, rel)
		if len(result.Findings) == 0 {
			return copyRegular(p, target)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, redact.RedactFile(data), 0600); err != nil {
			return err
		}
		redacted++
		files++
		total += int64(len(data))
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	fmt.Printf("✓ Redacted %s → %s (%d файлов, из них redacted %d)\n", source, out, files, redacted)
	printUnscanned(unscanned)
	logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_apply",
		Message: "Redaction применена", Data: map[string]any{"source": source, "out": out,
			"files": files, "redacted": redacted, "bytes": total,
			"unscanned": countUnscanned(unscanned)}, Exit: exitOK})
	return nil
}

// printUnscanned выводит участки, которые сканер не разобрал. Пустой вывод
// означает «просканировано всё», и только тогда «находок нет» = «секретов
// нет» (QS-08).
func printUnscanned(unscanned []redact.UnscannedFile) {
	if len(unscanned) == 0 {
		return
	}
	fmt.Printf("⚠ Не просканировано: %d участков в %d файлах (чистота не подтверждена)\n",
		countUnscanned(unscanned), len(unscanned))
	for _, u := range unscanned {
		for _, item := range u.Items {
			fmt.Printf("  %s:%d не просканировано (%s): %s\n", u.Path, item.Line, item.Reason, item.Detail)
		}
	}
}

func countUnscanned(unscanned []redact.UnscannedFile) int {
	total := 0
	for _, u := range unscanned {
		total += len(u.Items)
	}
	return total
}

func copyRegular(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func redactReportData(report *redact.Report) map[string]any {
	if report == nil {
		return map[string]any{"verdict": "error"}
	}
	return map[string]any{
		"verdict":    report.Verdict,
		"files":      report.Files,
		"bytes":      report.Bytes,
		"violations": len(report.Violations),
		// QS-08: число непросканированных участков — часть машиночитаемого
		// вердикта, иначе «violations: 0» читается как «чисто».
		"unscanned": countUnscanned(report.Unscanned),
	}
}

// redactRoot — корень сканирования: .ai-team/runs, если он есть, иначе target.
func redactRoot(target string) (string, error) {
	runs := filepath.Join(target, ".ai-team", "runs")
	if _, err := os.Stat(runs); err == nil {
		return runs, nil
	}
	return target, nil
}

func validRunID(id string) bool {
	if id == "" || id == "." || id == ".." || id != filepath.Base(id) {
		return false
	}
	return !strings.ContainsAny(id, `/\:`+"*?[]{}")
}
