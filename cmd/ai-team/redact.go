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

// cmdRedact — P1-6 privacy-контракт:
//
//	ai-team redact verify --target <dir> [--run <id>] [-json listed]
//	ai-team redact scan   --target <dir> [--path <rel>]
//	ai-team redact redact --target <dir> --out <dir> [--path <rel>]
func cmdRedact() {
	redactFlags := flag.NewFlagSet("redact", flag.ExitOnError)
	target := redactFlags.String("target", ".", "Путь к целевому проекту")
	runID := redactFlags.String("run", "", "Ограничить скан одним run (run_id)")
	pathArg := redactFlags.String("path", "", "Ограничить скан repository-относительным путём")
	outDir := redactFlags.String("out", ".ai-team/redacted", "Каталог для redaction-копии (для redact)")
	redactFlags.Parse(os.Args[2:])

	if redactFlags.NArg() != 1 {
		fatal("Использование: ai-team redact < verify | scan | redact > [--target <dir>] [--run <id>] [--path <rel>]")
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
		scanRoot = filepath.Join(scanRoot, strings.TrimPrefix(filepath.FromSlash(*pathArg), "./"))
	}

	switch redactFlags.Arg(0) {
	case "verify":
		report, err := redact.Verify(scanRoot, policy)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ Redaction %s: %v\n", scanRoot, err)
			logging.Emit(logging.Record{Level: "error", Command: "redact", Type: "redact_verify",
				Message: err.Error(), Data: redactReportData(report)})
			os.Exit(exitFailed)
		}
		fmt.Printf("✓ Redaction %s: clean (%d файлов)\n", scanRoot, report.Files)
		logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_verify",
			Message: "Redaction clean", Data: redactReportData(report), Exit: exitOK})
	case "scan":
		found, scanned, total, err := redact.ScanDir(scanRoot, policy)
		if err != nil {
			fatal("Ошибка скана: %v", err)
		}
		report := redact.Report{Files: scanned, Bytes: total, Verdict: "clean"}
		for _, f := range found {
			report.Violations = append(report.Violations, redact.Violation{Path: f.Path, Findings: f.Findings})
		}
		if len(report.Violations) > 0 {
			for _, v := range report.Violations {
				for _, f := range v.Findings {
					fmt.Printf("  %s:%d %s (%s)\n", v.Path, f.Line, f.Matched, f.Reason)
				}
			}
		}
		logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_scan",
			Message: fmt.Sprintf("Scan: %d files, %d findings", scanned, len(report.Violations)),
			Data:    redactReportData(&report), Exit: exitOK})
	case "redact":
		applyRedact(scanRoot, *outDir, policy)
	default:
		fatal("Неизвестная подкоманда redact: %s", redactFlags.Arg(0))
	}
}

func applyRedact(source, out string, policy redact.Policy) {
	if _, err := os.Stat(source); err != nil {
		fatal("redact: источник не найден: %v", err)
	}
	if !filepath.IsAbs(out) {
		abs, err := filepath.Abs(out)
		if err != nil {
			fatal("redact: out path: %v", err)
		}
		out = abs
	}
	if _, err := os.Stat(out); err == nil {
		fatal("redact: целевой каталог уже существует: %s", out)
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		fatal("redact: создание out: %v", err)
	}
	var files, redacted, total int64
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
		if !policy.Applies(filepath.ToSlash(rel)) {
			return nil
		}
		findings, err := redact.ScanFile(p, policy.MaxBytes)
		if err != nil {
			return err
		}
		target := filepath.Join(out, rel)
		if len(findings) == 0 {
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
		fatal("redact: %v", walkErr)
	}
	fmt.Printf("✓ Redacted %s → %s (%d файлов, из них redacted %d)\n", source, out, files, redacted)
	logging.Emit(logging.Record{Level: "ok", Command: "redact", Type: "redact_apply",
		Message: "Redaction применена", Data: map[string]any{"source": source, "out": out,
			"files": files, "redacted": redacted, "bytes": total}, Exit: exitOK})
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
