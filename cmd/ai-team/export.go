package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/export"
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
	if err := exportFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов export: %v", err)
	}
	if exportFlags.NArg() != 1 {
		fatal("Использование: ai-team export [--target <dir>] [--out <path>] <run_id>")
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

	fmt.Printf("✓ Run %s проверенно экспортирован\n", runID)
	fmt.Printf("  bundle:        %s\n", outDir)
	fmt.Printf("  bundle_sha256: %s\n", bundleSHA)
	fmt.Printf("  verified:      state/exports/%s.json (разрешает gc --prune-runs)\n", runID)
}
