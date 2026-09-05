package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
	"github.com/arturpanteleev/ai-team/pkg/gate"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/redact"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// cmdGate реализует `ai-team gate` MVP (V0-5): детерминированный diff-policy
// вердикт для trusted local base/candidate, typed checks и самодостаточный
// attestation bundle со стабильными exit codes. Работает без .ai-team и без
// runtime — дешёвый вход для внешних проектов. untrusted заблокирован:
// любой неразрешаемый в локальный объект ref даёт BLOCKED (exit 2).
func cmdGate() {
	gateFlags := flag.NewFlagSet("gate", flag.ExitOnError)
	target := gateFlags.String("target", ".", "Путь к целевому проекту")
	baseRef := gateFlags.String("base", "HEAD", "Базовый ref (только trusted local)")
	candidateRef := gateFlags.String("candidate", "WORKTREE", "Кандидат: локальный ref или WORKTREE")
	configPath := gateFlags.String("config", "", "Путь gate config (по умолчанию gate.yaml в target, затем .ai-team/gate.yaml)")
	out := gateFlags.String("out", "", "Каталог attestation bundle (по умолчанию <target>/.ai-team/gates/<ts> или gate-out/<ts>)")
	allowUntrusted := gateFlags.Bool("allow-untrusted", false, "Запрещено до P1-4")
	signKey := gateFlags.String("sign-key", "", "Путь к ed25519 private key (PEM PKCS8 или raw) для DSSE-подписи bundle (P1-5)")
	if err := gateFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов gate: %v", err)
	}
	if gateFlags.NArg() != 0 {
		fatal("Неожиданные аргументы gate: %s", strings.Join(gateFlags.Args(), " "))
	}

	var privKey ed25519.PrivateKey
	if *signKey != "" {
		key, err := dsse.LoadPrivateKey(*signKey)
		if err != nil {
			fatal("Ошибка загрузки signing key: %v", err)
		}
		privKey = key
	}

	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absolute

	cfg, usedConfig := loadGateConfig(*target, *configPath)
	if usedConfig != "" {
		logging.Printf("Gate config: %s\n", usedConfig)
	}

	if *out == "" {
		*out = defaultGateOut(*target)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, code, err := gate.Run(ctx, gate.Options{
		TargetDir: *target, Base: *baseRef, Candidate: *candidateRef,
		Config: cfg, AllowUntrusted: *allowUntrusted,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Gate: %v\n", err)
		os.Exit(code)
	}

	// AUD-11: privacy guard обязателен ДО публикации и подписи bundle —
	// gate-записи содержат captured Stdout/Stderr проверок, которые могут
	// нести credential. Политика та же fail-closed, что и у export.
	policyCfg, cfgErr := loadPolicyConfig(*target)
	if cfgErr != nil {
		fatal("Ошибка загрузки конфига: %v", cfgErr)
	}
	policy := redactionPolicy(policyCfg)
	policy.RepoRoot = *target

	if err := publishGateBundle(*out, result, policy, privKey); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Gate bundle: %v\n", err)
		os.Exit(exitBlocked)
	}
	printGateSummary(result, *out)
	if logging.GetMode() == logging.ModeJSON || logging.GetMode() == logging.ModeQuiet {
		logging.Emit(logging.Record{
			Level: "ok", Command: "gate", Type: "gate",
			Message: "Gate вердикт",
			Data: map[string]any{
				"status":        result.Status,
				"policy":        result.PolicyVerdict,
				"bundle_sha256": result.BundleSHA256,
				"bundle":        *out,
				"signed":        privKey != nil,
			},
			Exit: gate.ExitCode(result),
		})
	}
	os.Exit(gate.ExitCode(result))
}

// publishGateBundle записывает attestation bundle в staging-каталог, прогоняет
// privacy scan (fail-closed, AUD-11) по сериализованным records и только при
// чистом вердикте подписывает и атомарно публикует в outDir. Сигнатура (DSSE)
// всегда вычисляется по окончательным безопасным records; при секретах ничего
// не публикуется. Существующий outDir отклоняется (bundle immutable).
func publishGateBundle(outDir string, result *gate.Result, policy redact.Policy, privKey ed25519.PrivateKey) error {
	if _, err := os.Stat(outDir); err == nil {
		return fmt.Errorf("bundle %s уже существует", outDir)
	}
	if err := os.MkdirAll(filepath.Dir(outDir), 0755); err != nil {
		return fmt.Errorf("staging каталог: %w", err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(outDir), ".gate-bundle-")
	if err != nil {
		return fmt.Errorf("staging каталог: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := gate.WriteBundle(tmp, result); err != nil {
		return err
	}
	report, redactErr := redact.Verify(tmp, policy)
	if redactErr != nil {
		return fmt.Errorf("bundle содержит секреты: %w", redactErr)
	}
	if len(report.Violations) > 0 {
		logging.Emit(logging.Record{Level: "warn", Command: "gate", Type: "redact_findings",
			Message: "Секреты найдены, но политика разрешает публикацию",
			Data:    map[string]any{"violations": len(report.Violations)}})
	}
	if privKey != nil {
		if err := gate.SignBundle(tmp, privKey); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, outDir); err != nil {
		return fmt.Errorf("публикация bundle: %w", err)
	}
	return nil
}

// loadGateConfig загружает gate config: заданный --config, либо gate.yaml в
// target, либо .ai-team/gate.yaml. При отсутствии конфига — безопасные
// встроенные дефолты (test_modify required, без checks).
func loadGateConfig(target, explicit string) (*gate.Config, string) {
	if explicit != "" {
		cfg, err := gate.LoadConfig(explicit)
		if err != nil {
			fatal("Gate config %s: %v", explicit, err)
		}
		return cfg, explicit
	}
	for _, candidate := range []string{
		filepath.Join(target, "gate.yaml"),
		filepath.Join(target, ".ai-team", "gate.yaml"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			cfg, err := gate.LoadConfig(candidate)
			if err != nil {
				fatal("Gate config %s: %v", candidate, err)
			}
			return cfg, candidate
		}
	}
	fmt.Fprintln(os.Stderr, "⚠ Gate config не найден; используются дефолты: diff_policy test_modify=required, checks не заданы")
	return &gate.Config{SchemaVersion: gate.SchemaVersion, DiffPolicy: gate.DiffPolicy{TestModify: gate.TestModifyRequired}}, ""
}

func defaultGateOut(target string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	if _, err := safeio.ExistingDir(target, ".ai-team"); err == nil {
		return filepath.Join(target, ".ai-team", "gates", ts)
	}
	return filepath.Join("gate-out", ts)
}

// abbrev12 усекает commit sha до 12 символов; не-sha значения (WORKTREE)
// печатаются как есть.
func abbrev12(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func printGateSummary(result *gate.Result, outDir string) {
	byClass := make(map[string]int)
	for _, mutation := range result.Mutations {
		byClass[mutation.Class]++
	}
	classes := make([]string, 0, len(byClass))
	for class := range byClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	parts := make([]string, 0, len(classes))
	for _, class := range classes {
		parts = append(parts, fmt.Sprintf("%s=%d", class, byClass[class]))
	}

	mark := "✓"
	status := "PASS"
	if result.Status != "passed" {
		mark, status = "✗", "FAIL"
	}
	logging.Printf("\n%s Gate %s (exit %d) — %s\n", mark, status, gate.ExitCode(result), strings.Join(parts, ", "))

	candidateRef := result.Candidate
	if result.CandidateCommit != "" {
		candidateRef = result.CandidateCommit
	}
	logging.Printf("  base %s → %s (%s)\n", abbrev12(result.BaseCommit), abbrev12(candidateRef), result.DiffPolicy)

	switch result.PolicyVerdict {
	case gate.VerdictPassed:
		logging.Printf("  diff-policy: PASS — test_modify=%s, test change покрывает source\n", result.DiffPolicy)
	case gate.VerdictViolated:
		logging.Printf("  diff-policy: FAIL — изменение source без тестов (test_modify=%s):\n", result.DiffPolicy)
		for _, violation := range result.PolicyViolations {
			logging.Printf("    - %s (%s)\n", violation.Path, violation.Kind)
		}
	case gate.VerdictWarning:
		logging.Printf("  diff-policy: WARNING — изменение source без тестов (test_modify=warning, не блокирует):\n")
		for _, violation := range result.PolicyViolations {
			logging.Printf("    - %s (%s)\n", violation.Path, violation.Kind)
		}
	case gate.VerdictSkipped:
		logging.Printf("  diff-policy: disabled\n")
	}

	if len(result.Checks) == 0 {
		logging.Printf("  checks: не заданы\n")
	}
	for _, check := range result.Checks {
		statusMark := "✓"
		if check.Status != checks.StatusPassed {
			statusMark = "✗"
		}
		logging.Printf("  check %-16s %-8s %s (%s)\n", check.Name, statusMark, check.Status, time.Duration(check.Duration).Round(time.Millisecond))
	}

	sig := result.Signals
	logging.Printf("  signals: +%d/-%d lines, %d→%d files, tests=%d, checks=%d failed=%d",
		sig.AddedLines, sig.RemovedLines, sig.AddedFiles+sig.ModifiedFiles, sig.RemovedFiles, sig.TestChanges, sig.ChecksRun, sig.FailedChecks)
	if len(sig.SensitivePaths) > 0 {
		paths := make([]string, 0, len(sig.SensitivePaths))
		for _, entry := range sig.SensitivePaths {
			paths = append(paths, entry.Path)
		}
		logging.Printf("; sensitive: %s", strings.Join(paths, ", "))
	}
	fmt.Println()

	logging.Printf("  bundle: %s\n  bundle digest: %s\n", outDir, result.BundleSHA256)
}
