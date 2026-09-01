package main

import (
	"crypto/ed25519"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/dsse"
	"github.com/arturpanteleev/ai-team/pkg/export"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/redact"
)

// cmdExport собирает проверенный portable bundle терминального run (V0-4):
// whitelisted typed records и digests без raw logs/stdout. Bundle проходит
// полную verify и только после этого публикуется verified-запись в
// state/exports/<runID>.json (контракт V0-0) — она открывает право
// `gc --prune-runs` удалить evidence run'а.
func cmdExport() {
	exportFlags := flag.NewFlagSet("export", flag.ExitOnError)
	target := exportFlags.String("target", ".", "Путь к целевому проекту")
	out := exportFlags.String("out", "", "Каталог bundle (по умолчанию .ai-team/exports/<run_id>.bundle)")
	signKey := exportFlags.String("sign-key", "", "Путь к ed25519 private key (PEM PKCS8 или raw) для DSSE-подписи bundle (P1-5)")
	if err := exportFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов export: %v", err)
	}
	if exportFlags.NArg() != 1 {
		fatal("Использование: ai-team export [--target <dir>] [--out <path>] [--sign-key <path>] <run_id>")
	}

	var privKey ed25519.PrivateKey
	if *signKey != "" {
		key, err := dsse.LoadPrivateKey(*signKey)
		if err != nil {
			fatal("Ошибка загрузки signing key: %v", err)
		}
		privKey = key
	}
	runID := exportFlags.Arg(0)
	if runID == "" || runID == "." || runID == ".." || filepath.Base(runID) != runID {
		fatal("недопустимый run_id %q", runID)
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)

	runDir := filepath.Join(absolute, ".ai-team", "runs", runID)
	if _, err := os.Stat(filepath.Join(runDir, "anchor.json")); err != nil {
		fatal("run %s не является терминальным (нет anchor.json): %v", runID, err)
	}

	// P1-6: обязательный fail-closed блокер перед внешней публикацией bundle.
	// Secrets-скан evidence run'а; при находках (и политике по умолчанию)
	// экспорт отклоняется. From config: redaction.include/exclude +
	// redaction.disable_export_block. Отсутствие конфига — default-политика.
	cfg, cfgErr := loadPolicyConfig(absolute)
	if cfgErr != nil {
		fatal("Ошибка загрузки конфига: %v", cfgErr)
	}
	policy := redactionPolicy(cfg)
	scanRoot := runDir
	report, redactErr := redact.Verify(scanRoot, policy)
	if redactErr != nil {
		fmt.Fprintf(os.Stderr, "✗ Export blocked: evidence run %s содержит секреты: %v\n", runID, redactErr)
		logging.Emit(logging.Record{Level: "error", Command: "export", Type: "redact_block",
			Message: redactErr.Error(), Data: map[string]any{"run_id": runID, "blocked": true}})
		fatal("Экспорт заблокирован: redaction не прошла (см. ai-team redact verify --run %s)", runID)
	}
	if len(report.Violations) > 0 {
		logging.Emit(logging.Record{Level: "warn", Command: "export", Type: "redact_findings",
			Message: "Секреты найдены, но политика разрешает экспорт",
			Data:    map[string]any{"run_id": runID, "violations": len(report.Violations)}})
	}

	outDir := *out
	if outDir == "" {
		outDir = filepath.Join(absolute, ".ai-team", "exports", runID+".bundle")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(absolute, outDir)
	}
	outDir = filepath.Clean(outDir)
	if _, err := os.Stat(outDir); err == nil {
		fatal("bundle %s уже существует", outDir)
	}
	if err := os.MkdirAll(filepath.Dir(outDir), 0755); err != nil {
		fatal("Ошибка создания каталога bundle: %v", err)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(outDir), ".bundle-"+runID+"-")
	if err != nil {
		fatal("Ошибка staging bundle: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()

	index, err := export.Build(runDir, tmp)
	if err != nil {
		fatal("Ошибка экспорта: %v", err)
	}
	if err := export.VerifyBundle(tmp); err != nil {
		fatal("Экспорт не прошёл полную проверку (evidence повреждена?): %v", err)
	}
	if privKey != nil {
		if err := export.SignBundle(tmp, privKey); err != nil {
			fatal("Ошибка подписи bundle: %v", err)
		}
	}
	if err := os.Rename(tmp, outDir); err != nil {
		fatal("Не удалось зафиксировать bundle: %v", err)
	}
	committed = true

	bundleSHA, err := export.BundleDigest(index)
	if err != nil {
		fatal("Не удалось вычислить bundle_sha256: %v", err)
	}
	if err := export.PublishVerified(filepath.Join(absolute, ".ai-team"), runID, outDir, bundleSHA, time.Now().UTC()); err != nil {
		fatal("Не удалось записать verified-запись state/exports: %v", err)
	}

	logging.Printf("✓ Run %s проверенно экспортирован\n", runID)
	logging.Printf("  bundle:        %s\n", outDir)
	logging.Printf("  bundle_sha256: %s\n", bundleSHA)
	if privKey != nil {
		logging.Printf("  signed:        DSSE ed25519 (dsse.json)\n")
	}
	logging.Printf("  verified:      state/exports/%s.json (разрешает gc --prune-runs)\n", runID)
	if logging.GetMode() == logging.ModeJSON || logging.GetMode() == logging.ModeQuiet {
		logging.Emit(logging.Record{
			Level: "ok", Command: "export", Type: "bundle",
			Message: "Экспорт выполнен",
			Data: map[string]any{
				"run_id": runID, "bundle": outDir,
				"bundle_sha256": bundleSHA, "signed": false,
			},
			Exit: 0,
		})
	}
}
