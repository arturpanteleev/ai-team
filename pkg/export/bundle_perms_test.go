package export

import (
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/dsse"
)

// PDD-25: все файлы run bundle (index.json наравне со скопированными records
// и dsse.json) записываются одинаковыми правами без бита записи.
func TestRunBundleFilesAreReadOnly(t *testing.T) {
	base := t.TempDir()
	runDir := buildTerminalRun(t, filepath.Join(base, "runs"))
	bundle := filepath.Join(base, "bundle")
	if _, err := Build(runDir, bundle); err != nil {
		t.Fatalf("build: %v", err)
	}
	// Подписание идёт после записи индекса: read-only права файлов не мешают
	// добавить dsse.json в каталог bundle.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignBundle(bundle, priv); err != nil {
		t.Fatalf("SignBundle после read-only index.json: %v", err)
	}
	if err := VerifyBundle(bundle); err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	modes := make(map[string]os.FileMode)
	walkErr := filepath.WalkDir(bundle, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		rel, relErr := filepath.Rel(bundle, path)
		if relErr != nil {
			return relErr
		}
		modes[filepath.ToSlash(rel)] = info.Mode().Perm()
		return nil
	})
	if walkErr != nil {
		t.Fatalf("обход bundle: %v", walkErr)
	}
	for _, name := range []string{indexFileName, "run.json", "events.jsonl", "anchor.json", dsse.EnvelopeFileName} {
		if _, ok := modes[name]; !ok {
			t.Fatalf("в bundle нет файла %s (найдено: %v)", name, modes)
		}
	}
	// Точное значение прав зависит от umask процесса, поэтому файлы
	// сравниваются между собой, а не с литералом.
	var reference os.FileMode
	referenceName := ""
	for name, mode := range modes {
		if mode&0o222 != 0 {
			t.Fatalf("файл bundle %s доступен на запись (%04o)", name, mode)
		}
		if referenceName == "" {
			reference, referenceName = mode, name
			continue
		}
		if mode != reference {
			t.Fatalf("права файлов bundle неоднородны: %s=%04o, %s=%04o",
				referenceName, reference, name, mode)
		}
	}
}
