package gate

import (
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/dsse"
)

// PDD-25: все файлы bundle (index.json наравне с gate.json, checks/*.json и
// dsse.json) записываются одинаковыми правами без бита записи. Неоднородность
// прав читалась бы как недосмотр ровно там, где продукт демонстрирует
// аккуратность к неизменяемости; фактическую неизменяемость даёт digest.
func TestGateBundleFilesAreReadOnly(t *testing.T) {
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	result := &Result{
		SchemaVersion: SchemaVersion, Base: "HEAD", Candidate: "HEAD",
		BaseCommit: "aabb", CandidateCommit: "ccdd",
		BaseTree: "tree-a", CandidateTree: "tree-b",
		DiffPolicy:    TestModifyRequired,
		Mutations:     []Mutation{{Path: "src/app.go", Kind: KindModified, Class: "source"}},
		PolicyVerdict: VerdictPassed, Status: "passed", FinishedAt: fixed,
		Checks: []checks.Result{{
			Name: "go-test", Class: "unit", Adapter: checks.AdapterGoTest,
			Command: []string{"go", "test", "-json", "./..."}, Policy: checks.PolicyRequired,
			ExitCode: 0, Status: checks.StatusPassed, StartedAt: fixed, FinishedAt: fixed,
		}},
	}
	dir := t.TempDir()
	if err := WriteBundle(dir, result); err != nil {
		t.Fatal(err)
	}
	// Подписание идёт после записи индекса: read-only права файлов не мешают
	// добавить dsse.json в каталог bundle.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignBundle(dir, priv); err != nil {
		t.Fatalf("SignBundle после read-only index.json: %v", err)
	}
	if _, err := VerifyBundle(dir); err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	assertBundleReadOnly(t, dir, []string{
		"index.json", "gate.json", "checks/001-go-test.json", dsse.EnvelopeFileName,
	})
}

// assertBundleReadOnly проверяет, что каждый обычный файл bundle записан без
// бита записи и что права всех файлов одинаковы (точное значение зависит от
// umask процесса, поэтому сравниваются между собой, а не с литералом).
func assertBundleReadOnly(t *testing.T, bundleDir string, expected []string) {
	t.Helper()
	modes := make(map[string]os.FileMode)
	err := filepath.WalkDir(bundleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		rel, relErr := filepath.Rel(bundleDir, path)
		if relErr != nil {
			return relErr
		}
		modes[filepath.ToSlash(rel)] = info.Mode().Perm()
		return nil
	})
	if err != nil {
		t.Fatalf("обход bundle: %v", err)
	}
	for _, name := range expected {
		if _, ok := modes[name]; !ok {
			t.Fatalf("в bundle нет файла %s (найдено: %v)", name, modes)
		}
	}
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
