package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadRegularFile reads a bounded regular file and rejects symlinks, devices,
// FIFOs and path replacement between lstat and open.
func ReadRegularFile(path string, maxBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s должен быть regular file без symlink", path)
	}
	if maxBytes <= 0 || before.Size() > maxBytes {
		return nil, fmt.Errorf("%s превышает лимит %d bytes", path, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s изменён во время безопасного открытия", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s превышает лимит %d bytes", path, maxBytes)
	}
	return data, nil
}

// WriteRegularFileNoFollow пишет data в path, гарантируя no-follow на всей
// цепочке пути (AUD-04): родительские каталоги проходятся по одному компоненту
// (существующие обязаны быть обычными каталогами без symlink, отсутствующие
// создаются os.Mkdir), листовой symlink отклоняется до создания, а сам файл
// создаётся строго через O_CREATE|O_EXCL — существующий файл даёт ошибку,
// разыменование невозможно. Иммутабельные контроллерные артефакты (bundle
// records) не перезаписываются.
func WriteRegularFileNoFollow(path string, data []byte, mode os.FileMode) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := ensureNoFollowDirChain(filepath.Dir(abs)); err != nil {
		return err
	}
	// Листовой symlink (или special файл) отвергается до создания файла:
	// sentinel за целевой записью не изменяется.
	if info, lerr := os.Lstat(abs); lerr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("controller file %s является symlink: запись отклонена (AUD-04)", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("controller file %s не является regular file", path)
		}
		return fmt.Errorf("controller file %s уже существует: immutable-запись запрещена (AUD-04)", path)
	} else if !os.IsNotExist(lerr) {
		return lerr
	}
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("controller file %s уже существует: immutable-запись запрещена (AUD-04)", path)
		}
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// EnsureDirPath создаёт каталог (и недостающие промежуточные) компонентно,
// отклоняя symlink в любом компоненте цепочки. Отличается от os.MkdirAll:
// никакой компонент не обходится как symlink.
func EnsureDirPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return ensureNoFollowDirChain(abs)
}

// SystemBootstrapRedirectPaths — штатные Darwin-редиректы корневых каталогов,
// консолидируемых под /private (см. man hier, macOS): /var, /tmp, /etc и сам
// /private на некоторых конфигурациях. Только эти пути (ровно один сегмент на
// корневом уровне) разрешены как bootstrap redirect; любое другое вхождение
// `X -> private/X` (например, поддельный symlink внутри рабочего дерева)
// отклоняется как признак ухода за пределы доверенной зоны (F-5).
var SystemBootstrapRedirectPaths = map[string]string{
	"/var": "/private/var",
	"/tmp": "/private/tmp",
	"/etc": "/private/etc",
}

// isSystemBootstrapRedirect распознаёт штатные OS-редиректы начальных каталогов
// (macOS: /var → private/var, /tmp → private/tmp, /etc → private/etc — Darwin
// консолидирует их под /private). Разрешается БЕЗУСЛОВНО только точный
// системный redirect на корневом уровне: канонический префикс обязан быть
// ровно /private/<basename>. Все копии такого вида глубже в цепочке (напр.
// созданный атакуемым `out -> private/out` внутри рабочего дерева) НЕ
// считаются системными и отклоняются ниже (F-5).
func isSystemBootstrapRedirect(path string, info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	canonical, ok := SystemBootstrapRedirectPaths[path]
	if !ok {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	// Ссылка должна указывать ровно на канонический системный каталог
	// (private/<base> либо /private/<base>), а не на какой-то промежуточный.
	base := filepath.Base(path)
	expected := filepath.Join(string(filepath.Separator)+"private", base)
	if target != filepath.Join("private", base) && target != expected {
		return false
	}
	resolved, rerr := filepath.EvalSymlinks(path)
	if rerr != nil {
		return false
	}
	return filepath.Clean(resolved) == canonical
}

// ensureNoFollowDirChain проходит каждый компонент abs-каталога сверху вниз.
func ensureNoFollowDirChain(abs string) error {
	root := filepath.VolumeName(abs)
	rest := abs
	if root != "" {
		rest = rest[len(root):]
	}
	rest = strings.TrimLeft(rest, string(filepath.Separator))
	current := root
	if current == "" {
		current = string(filepath.Separator)
	}
	for _, component := range strings.Split(rest, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case os.IsNotExist(err):
			if mkErr := os.Mkdir(current, 0755); mkErr != nil && !os.IsExist(mkErr) {
				return mkErr
			}
			if reqErr := requireDirectory(current); reqErr != nil {
				return reqErr
			}
		case err != nil:
			return err
		default:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				if isSystemBootstrapRedirect(current, info) {
					resolved, rerr := filepath.EvalSymlinks(current)
					if rerr != nil {
						return rerr
					}
					current = resolved
					continue
				}
				return fmt.Errorf("controller path %s должен быть каталогом без symlink", current)
			}
		}
	}
	return nil
}
