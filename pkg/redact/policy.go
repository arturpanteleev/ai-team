package redact

import (
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/scope"
)

// ScanDefaults — канонические параметры сканирования.
const (
	MaxScanFileBytes = 8 << 20 // 8 MiB на файл
)

// Policy — конфигурируемая политика redaction (include/exclude на
// repository-относительные glob'ы + raw logs opt-in).
type Policy struct {
	// Include — если непусто, сканируются только matching-файлы (значение —
	// repository-относительные пути, slash-separated). Пусто = все файлы.
	Include []string
	// Exclude — matching-файлы исключаются из сканирования.
	Exclude []string
	// FailOnSecrets — fail-closed: наличие находок делает Verify/export
	// проваленным. При false находки только регистрируются.
	FailOnSecrets bool
	// MaxBytes — предел размера сканируемого файла.
	MaxBytes int64
	// SkipBinary — не сканировать бинарные файлы.
	SkipBinary bool
	// RepoRoot — корень репозитория: протокол-относительный glob'ы
	// include/exclude резолвятся от него, а не от суженного корня сканирования
	// (.ai-team/runs или каталог одного run). Пустое значение = корнем служит
	// сам корень сканирования (совместимость с узкими вызовами).
	RepoRoot string
}

func (p Policy) withDefaults() Policy {
	if p.MaxBytes <= 0 {
		p.MaxBytes = MaxScanFileBytes
	}
	return p
}

// Applies определяет, должен ли файл (repository-относительный путь с '/')
// попадать в обзор сканирования по текущей политике.
func (p Policy) Applies(relPath string) bool {
	if len(p.Exclude) > 0 && scope.MatchAny(p.Exclude, relPath) {
		return false
	}
	if len(p.Include) > 0 {
		return scope.MatchAny(p.Include, relPath)
	}
	return true
}

// Validate проверяет include/exclude glob'ы (repository-relative, не '.'/'..').
func (p Policy) Validate() error {
	for _, g := range p.Include {
		if err := scope.Validate(g); err != nil {
			return fmt.Errorf("redaction.include %q: %w", g, err)
		}
	}
	for _, g := range p.Exclude {
		if err := scope.Validate(g); err != nil {
			return fmt.Errorf("redaction.exclude %q: %w", g, err)
		}
	}
	return nil
}
