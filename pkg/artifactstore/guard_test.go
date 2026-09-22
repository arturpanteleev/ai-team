package artifactstore

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guard_test.go — issue #119: artifactstore хранит immutable run artifacts,
// поэтому проверяются именно guard'ы хранилища — валидация digest и run ID,
// целостность блобов при повторной записи и невозможность вывести restore
// за пределы каталога назначения.

func newCAS(t *testing.T) *LocalCAS {
	t.Helper()
	store, err := NewLocalCAS(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestNewLocalCASRejectsUnsafeRoot — корень CAS обязан быть каталогом:
// файл или symlink на его месте означает, что хранилище не наше.
func TestNewLocalCASRejectsUnsafeRoot(t *testing.T) {
	base := t.TempDir()
	asFile := filepath.Join(base, "cas-file")
	if err := os.WriteFile(asFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalCAS(asFile); err == nil {
		t.Fatal("файл на месте корня CAS обязан отклоняться")
	}

	link := filepath.Join(base, "cas-link")
	if err := os.Symlink(filepath.Join(base, "elsewhere"), link); err != nil {
		t.Skipf("symlink недоступен: %v", err)
	}
	if _, err := NewLocalCAS(link); err == nil {
		t.Fatal("symlink на месте корня CAS обязан отклоняться")
	}
}

// TestBlobPathRejectsInvalidDigest — digest приходит из manifest и
// подставляется в путь: всё, кроме 64 строчных hex-символов, обязано
// отклоняться до обращения к файловой системе.
func TestBlobPathRejectsInvalidDigest(t *testing.T) {
	store := newCAS(t)
	for _, bad := range []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64),
		strings.Repeat("z", 64),
		"../" + strings.Repeat("a", 61),
	} {
		if _, err := store.blobPath(bad); err == nil {
			t.Fatalf("digest %q обязан отклоняться", bad)
		}
		if _, err := store.WriteTo(bad, io.Discard); err == nil {
			t.Fatalf("WriteTo с digest %q обязан отклоняться", bad)
		}
	}
	blob, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.blobPath(blob.SHA256); err != nil {
		t.Fatalf("валидный digest отклонён: %v", err)
	}
}

// TestPutIsIdempotentAndVerifiesExistingBlob — повторный Put того же
// содержимого не должен переписывать immutable блоб, но обязан проверить,
// что лежащий в хранилище блоб не испорчен.
func TestPutIsIdempotentAndVerifiesExistingBlob(t *testing.T) {
	store := newCAS(t)
	first, err := store.Put(strings.NewReader("immutable payload"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(strings.NewReader("immutable payload"))
	if err != nil {
		t.Fatalf("повторный Put того же содержимого: %v", err)
	}
	if first != second {
		t.Fatalf("дедупликация нарушена: %+v != %+v", first, second)
	}

	// Порча блоба на диске обязана обнаруживаться при повторном Put: иначе
	// хранилище тихо подтвердит наличие данных, которых там больше нет.
	path, err := store.blobPath(first.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(strings.NewReader("immutable payload")); err == nil {
		t.Fatal("Put обязан отклонить испорченный существующий блоб")
	}
}

// TestManifestRejectsUnsafeRunID — run ID подставляется в имя файла
// manifests/<id>.json.
func TestManifestRejectsUnsafeRunID(t *testing.T) {
	store := newCAS(t)
	for _, bad := range []string{"", "../escape", "a/b", `a\b`} {
		if err := store.WriteManifest(bad, []byte("{}")); err == nil {
			t.Fatalf("WriteManifest с run ID %q обязан отклоняться", bad)
		}
		if _, err := store.ReadManifest(bad, 1<<20); err == nil {
			t.Fatalf("ReadManifest с run ID %q обязан отклоняться", bad)
		}
	}
}

// TestReadManifestRejectsTamperedReference — reference-файл manifest'а
// связывает run ID с digest и размером; расхождение любого поля означает
// подмену и обязано отклоняться, а не приводить к чтению чужих данных.
func TestReadManifestRejectsTamperedReference(t *testing.T) {
	store := newCAS(t)
	const runID = "run-manifest"
	payload := []byte(`{"schema_version":1}`)
	if err := store.WriteManifest(runID, payload); err != nil {
		t.Fatal(err)
	}
	referencePath := filepath.Join(store.root, "manifests", runID+".json")
	original, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}

	data, err := store.ReadManifest(runID, 1<<20)
	if err != nil {
		t.Fatalf("честный manifest должен читаться: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("manifest прочитан неверно: %q", data)
	}

	// Лимит меньше записанного размера: чтение обязано отказать, а не
	// молча отдать усечённые данные.
	if _, err := store.ReadManifest(runID, 4); err == nil {
		t.Fatal("manifest сверх лимита обязан отклоняться")
	}

	var reference map[string]any
	if err := json.Unmarshal(original, &reference); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"чужой run_id":    func(r map[string]any) { r["run_id"] = "other" },
		"чужая схема":     func(r map[string]any) { r["schema_version"] = 2 },
		"неверный размер": func(r map[string]any) { r["manifest_size"] = 999999 },
		"отрицательный размер": func(r map[string]any) {
			r["manifest_size"] = -1
		},
	}
	for name, mutate := range cases {
		tampered := make(map[string]any, len(reference))
		for key, value := range reference {
			tampered[key] = value
		}
		mutate(tampered)
		encoded, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(referencePath, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadManifest(runID, 1<<20); err == nil {
			t.Fatalf("%s: подменённый reference обязан отклоняться", name)
		}
	}

	// Мусор после JSON-объекта — тоже подмена.
	if err := os.WriteFile(referencePath, append(original, []byte("{}\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadManifest(runID, 1<<20); err == nil {
		t.Fatal("trailing JSON в reference обязан отклоняться")
	}

	// Неизвестное поле: reference читается строго.
	if err := os.WriteFile(referencePath, []byte(`{"schema_version":1,"run_id":"run-manifest","manifest_sha256":"","manifest_size":0,"extra":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadManifest(runID, 1<<20); err == nil {
		t.Fatal("неизвестное поле в reference обязано отклоняться")
	}
}

// TestRunArchiveRejectsUnsafeInput — архивируется только каталог run'а под
// известным корнем; всё остальное — либо ошибка вызова, либо попытка
// протащить в CAS то, чего там быть не должно.
func TestRunArchiveRejectsUnsafeInput(t *testing.T) {
	store := newCAS(t)
	if _, err := NewRunArchive(t.TempDir(), nil); err == nil {
		t.Fatal("архив без store обязан отклоняться")
	}

	runRoot := t.TempDir()
	archive, err := NewRunArchive(runRoot, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../escape", "a/b"} {
		if err := archive.Archive(bad); err == nil {
			t.Fatalf("Archive с run ID %q обязан отклоняться", bad)
		}
	}

	// Отсутствующий run — не ошибка: архивировать нечего.
	if err := archive.Archive("never-existed"); err != nil {
		t.Fatalf("отсутствующий run не должен быть ошибкой: %v", err)
	}

	// Файл вместо каталога run'а.
	if err := os.WriteFile(filepath.Join(runRoot, "not-a-dir"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := archive.Archive("not-a-dir"); err == nil {
		t.Fatal("файл на месте каталога run обязан отклоняться")
	}
}

// TestRestoreRejectsUnsafeManifestPaths — путь из manifest'а разворачивается
// в каталоге назначения. Абсолютный путь, выход через ".." и дубликат
// обязаны отклоняться: иначе восстановление чужого архива пишет куда угодно.
func TestRestoreRejectsUnsafeManifestPaths(t *testing.T) {
	store := newCAS(t)
	archive, err := NewRunArchive(filepath.Join(t.TempDir(), "runs"), store)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}

	writeManifest := func(t *testing.T, runID string, manifest Manifest) {
		t.Helper()
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WriteManifest(runID, data); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(path string) ManifestEntry {
		return ManifestEntry{Path: path, SHA256: blob.SHA256, Size: blob.Size, Mode: 0600}
	}

	cases := []struct {
		name    string
		runID   string
		entries []ManifestEntry
	}{
		{"абсолютный путь", "abs", []ManifestEntry{entry("/etc/passwd")}},
		{"выход через ..", "dotdot", []ManifestEntry{entry("../escape")}},
		{"неканоничный путь", "noncanonical", []ManifestEntry{entry("./nested/../file")}},
		{"дубликат пути", "duplicate", []ManifestEntry{entry("same"), entry("same")}},
		{"пустой путь", "empty", []ManifestEntry{entry("")}},
	}
	for _, testCase := range cases {
		writeManifest(t, testCase.runID, Manifest{
			SchemaVersion: ManifestVersion, RunID: testCase.runID,
			CreatedAt: time.Now().UTC(), Entries: testCase.entries,
		})
		destination := filepath.Join(t.TempDir(), "restored")
		if err := archive.Restore(testCase.runID, destination); err == nil {
			t.Fatalf("%s: restore обязан отклоняться", testCase.name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "escape")); err == nil {
			t.Fatalf("%s: restore записал файл вне каталога назначения", testCase.name)
		}
	}

	// Identity manifest'а обязана совпадать с запрошенным run.
	writeManifest(t, "identity", Manifest{
		SchemaVersion: ManifestVersion, RunID: "другой-run",
		CreatedAt: time.Now().UTC(), Entries: []ManifestEntry{entry("file")},
	})
	if err := archive.Restore("identity", filepath.Join(t.TempDir(), "restored")); err == nil {
		t.Fatal("manifest чужого run обязан отклоняться")
	}

	// Схема manifest'а тоже проверяется.
	writeManifest(t, "schema", Manifest{
		SchemaVersion: ManifestVersion + 1, RunID: "schema",
		CreatedAt: time.Now().UTC(), Entries: []ManifestEntry{entry("file")},
	})
	if err := archive.Restore("schema", filepath.Join(t.TempDir(), "restored")); err == nil {
		t.Fatal("manifest чужой схемы обязан отклоняться")
	}

	// Размер записи обязан совпадать с блобом.
	mismatched := entry("file")
	mismatched.Size = blob.Size + 1
	writeManifest(t, "size", Manifest{
		SchemaVersion: ManifestVersion, RunID: "size",
		CreatedAt: time.Now().UTC(), Entries: []ManifestEntry{mismatched},
	})
	destination := filepath.Join(t.TempDir(), "restored")
	if err := archive.Restore("size", destination); err == nil {
		t.Fatal("несовпадение размера обязано отклоняться")
	}
	if _, err := os.Stat(filepath.Join(destination, "file")); err == nil {
		t.Fatal("частично восстановленный файл не должен оставаться на диске")
	}

	// Отсутствующий manifest.
	if err := archive.Restore("never-archived", filepath.Join(t.TempDir(), "restored")); err == nil {
		t.Fatal("restore без manifest'а обязан отклоняться")
	}
}

// TestRestorePreservesNestedPathsAndModes — законный архив обязан
// восстанавливаться целиком, включая вложенные каталоги и права файлов.
func TestRestorePreservesNestedPathsAndModes(t *testing.T) {
	base := t.TempDir()
	runRoot := filepath.Join(base, "runs")
	source := filepath.Join(runRoot, "run-nested")
	if err := os.MkdirAll(filepath.Join(source, "attempts", "1", "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]os.FileMode{
		filepath.Join("anchor.json"):                      0644,
		filepath.Join("attempts", "1", "manifest.json"):   0600,
		filepath.Join("attempts", "1", "logs", "out.log"): 0640,
	}
	for relative, mode := range files {
		full := filepath.Join(source, relative)
		if err := os.WriteFile(full, []byte("содержимое "+relative), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}

	store := newCAS(t)
	archive, err := NewRunArchive(runRoot, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Archive("run-nested"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "restored")
	if err := archive.Restore("run-nested", destination); err != nil {
		t.Fatal(err)
	}
	for relative, mode := range files {
		info, err := os.Stat(filepath.Join(destination, relative))
		if err != nil {
			t.Fatalf("%s не восстановлен: %v", relative, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s: права %v, ожидались %v", relative, info.Mode().Perm(), mode)
		}
		expected, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("%s: содержимое расходится (%v)", relative, err)
		}
	}
}
