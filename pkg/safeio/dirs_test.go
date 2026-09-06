package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirRejectsSymlinkComponent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".ai-team")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := EnsureDir(root, ".ai-team", "runs"); err == nil {
		t.Fatal("symlink component must fail closed")
	}
}

func TestExistingDirDoesNotCreateAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if _, err := ExistingDir(root, ".ai-team"); err == nil {
		t.Fatal("missing control root must not be created")
	}
	if _, err := os.Stat(filepath.Join(root, ".ai-team")); !os.IsNotExist(err) {
		t.Fatalf("ExistingDir unexpectedly mutated target: %v", err)
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, ".ai-team")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ExistingDir(root, ".ai-team"); err == nil {
		t.Fatal("symlink control root must fail")
	}
}

func TestValidateTreeRejectsNestedSymlink(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "agents")
	if err := os.MkdirAll(filepath.Join(tree, "coder"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(tree, "coder", "prompt.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ValidateTree(tree); err == nil {
		t.Fatal("nested symlink must be rejected")
	}
}

// F-5: поддельный вид `X -> private/X` (как у системного bootstrap redirect)
// внутри рабочего дерева не должен пропускаться как системный redirect —
// иначе атакующий сводит запись наружу через свой каталог private.
func TestEnsureDirPathRejectsFakePrivateSymlinkRedirect(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// inside/private -> outside (атакующий контролирует и inside, и private).
	if err := os.MkdirAll(filepath.Join(root, "inside"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "inside", "private")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// inside/out -> private/out: ровно та форма, что у системных /var,/tmp,/etc.
	if err := os.Symlink("private/out", filepath.Join(root, "inside", "out")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := EnsureDirPath(filepath.Join(root, "inside", "out", "sink")); err == nil {
		t.Fatal("fake private/<basename> redirect must be rejected (would escape workspace)")
	}
}

// F-5: точный системный bootstrap redirect (корневой /var → private/var)
// распознаётся как системный и НЕ отклоняется; внутренние копии — никогда.
func TestSystemBootstrapRedirectOnlyRootLevel(t *testing.T) {
	if _, ok := SystemBootstrapRedirectPaths["/var"]; !ok {
		t.Fatal("/var должен быть в списке системных redirect-путей")
	}
	if _, ok := SystemBootstrapRedirectPaths["/tmp"]; !ok {
		t.Fatal("/tmp должен быть в списке системных redirect-путей")
	}
	if _, ok := SystemBootstrapRedirectPaths["/etc"]; !ok {
		t.Fatal("/etc должен быть в списке системных redirect-путей")
	}
	// Любой путь глубже корня не может быть системным redirect по контракту:
	// map keyed только по корневым /var,/tmp,/etc.
	if _, ok := SystemBootstrapRedirectPaths["/var/private/out"]; ok {
		t.Fatal("вложенные пути не должны считаться системными redirect")
	}
	if _, ok := SystemBootstrapRedirectPaths["/inside/out"]; ok {
		t.Fatal("каталоги рабочего дерева не должны считаться системными redirect")
	}
}

func TestRejectSymlink(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(root, "does-not-exist")
	if err := RejectSymlink(missing); err != nil {
		t.Fatalf("missing path must be valid (nothing to reject yet), got: %v", err)
	}

	regular := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(regular, []byte("schema_version: 3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RejectSymlink(regular); err != nil {
		t.Fatalf("regular file must be accepted, got: %v", err)
	}

	link := filepath.Join(root, "config-link.yaml")
	if err := os.Symlink(regular, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := RejectSymlink(link); err == nil {
		t.Fatal("symlink must be rejected")
	}

	dir := filepath.Join(root, "somedir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RejectSymlink(dir); err == nil {
		t.Fatal("non-regular file (directory) must be rejected")
	}
}

func TestReadRegularFileRejectsSymlinkAndLimit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(file, 4); err == nil {
		t.Fatal("oversized file must fail")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ReadRegularFile(link, 10); err == nil {
		t.Fatal("symlink must fail")
	}
}

func TestWriteRegularFileNoFollowRoundtrip(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "dir", "file")
	if err := WriteRegularFileNoFollow(target, []byte("data"), 0644); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := ReadRegularFile(target, 16)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("readback=%q", got)
	}
}

func TestWriteRegularFileNoFollowRejectsExistingFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "file")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRegularFileNoFollow(target, []byte("other"), 0444); err == nil {
		t.Fatal("existing file: immutable write должен FAIL")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatalf("existing file перезаписан: %q", got)
	}
}

func TestWriteRegularFileNoFollowRejectsLeafSymlink(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outfile")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteRegularFileNoFollow(link, []byte("evil"), 0644); err == nil {
		t.Fatal("leaf symlink: write должен FAIL (AUD-04)")
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "keep" {
		t.Fatalf("sentinel изменён через leaf symlink: %q", got)
	}
}

func TestWriteRegularFileNoFollowRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "0-sentinel")
	if err := os.Mkdir(sentinel, 0755); err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(root, "cyclic") // путь ниже следует не писать
	os.Mkdir(realDir, 0755)
	parent := filepath.Join(root, "sub")
	if err := os.Symlink(realDir, parent); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteRegularFileNoFollow(filepath.Join(parent, "file"), []byte("x"), 0644); err == nil {
		t.Fatal("parent symlink: write должен FAIL (AUD-04)")
	}
	// Запись не ушла в реальный каталог за symlink.
	if _, err := os.Lstat(filepath.Join(realDir, "file")); !os.IsNotExist(err) {
		t.Fatal("write попал в каталог за symlink")
	}
}

func TestWriteRegularFileNoFollowRejectsDeepSymlinkComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	deep := filepath.Join(root, "bundle")
	if err := os.Mkdir(deep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(deep, "checks")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteRegularFileNoFollow(filepath.Join(deep, "checks", "001-x"), []byte("x"), 0644); err == nil {
		t.Fatal("deep symlink component: write должен FAIL (AUD-04)")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("write ушёл за deep symlink: %v", entries)
	}
}
