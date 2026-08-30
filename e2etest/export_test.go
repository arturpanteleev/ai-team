package e2etest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2E_ExportAndVerifyBundle — реальный pipeline flow до терминального run,
// затем `ai-team export` (V0-4): bundle публикуется, verified-запись в
// state/exports, повторная самодостаточная verify (без repo и .ai-team) и
// локальная verify сходятся; tampering события ловится и вне repo.
func TestE2E_ExportAndVerifyBundle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	setupDeliveryGit(t, dir)

	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("ai-team init failed (%d):\n%s", code, out)
	}
	code, out := runAI(t, bin, dir, []string{pathEnv}, "run",
		"--feature", "e2e-export", "--task", "E2E export task", "--approve-gates")
	if code != 3 {
		t.Fatalf("run должен остановиться для delivery approval (exit 3), got %d:\n%s", code, out)
	}
	hashMatch := regexp.MustCompile(`Plan SHA-256: ([a-f0-9]{64})`).FindStringSubmatch(out)
	resumeMatch := regexp.MustCompile(`ai-team run --resume ([^ ]+) --approve-plan`).FindStringSubmatch(out)
	if len(hashMatch) != 2 || len(resumeMatch) != 2 {
		t.Fatalf("no delivery plan hash/resume in output:\n%s", out)
	}
	code, out = runAI(t, bin, dir, []string{pathEnv}, "run", "--resume", resumeMatch[1],
		"--approve-gates", "--approve-plan", hashMatch[1])
	if code != 0 {
		t.Fatalf("approved delivery retry failed (%d):\n%s", code, out)
	}

	runs, err := os.ReadDir(filepath.Join(dir, ".ai-team", "runs"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("ожидался один terminal run: %v", err)
	}
	runID := runs[0].Name()
	if code, out := runAI(t, bin, dir, nil, "verify", "--target", dir, runID); code != 0 {
		t.Fatalf("локальная verify terminal-раun не прошла (%d):\n%s", code, out)
	}

	bundleDir := filepath.Join(dir, ".ai-team", "exports", runID+".bundle")
	if code, out := runAI(t, bin, dir, nil, "export", "--target", dir, runID); code != 0 {
		t.Fatalf("export не прошёл (%d):\n%s", code, out)
	}
	for _, rel := range []string{
		"index.json", "run.json", "config.json", "workflow.json", "anchor.json",
		"attestation.json", "events.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(bundleDir, rel)); err != nil {
			t.Fatalf("bundle %s отсутствует: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".ai-team", "state", "exports", runID+".json")); err != nil {
		t.Fatalf("verified-запись не опубликована: %v", err)
	}

	if code, out := runAI(t, bin, dir, nil, "verify", bundleDir); code != 0 {
		t.Fatalf("самодостаточная verify bundle не прошла (%d):\n%s", code, out)
	}

	// Нейтральный каталог вне repo и .ai-team: bundle самоподписан.
	neutral := filepath.Join(t.TempDir(), "bundle-copy")
	if err := copyTree(bundleDir, neutral); err != nil {
		t.Fatalf("copy bundle: %v", err)
	}
	eventsPath := filepath.Join(neutral, "events.jsonl")
	if err := os.Chmod(eventsPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsPath, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tamperedCode, tamperedOut := runAI(t, bin, neutral, nil, "verify", neutral)
	if tamperedCode == 0 {
		t.Fatalf("verify обязан обнаружить tampering events в bundle:\n%s", tamperedOut)
	}
	if !strings.Contains(tamperedOut, "не совпадает") {
		t.Fatalf("ожидалась диагностика о подмене, получено:\n%s", tamperedOut)
	}
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
