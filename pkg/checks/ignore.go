package checks

import (
	"sync"
)

// Процесс-глобальный дополнительный ignore-набор (OPS-2). Один процесс
// управляет одним проектом (пользовательский CLI/daemon держит target под
// workspace lock), поэтому глобальная override на процесс безопасна и, главное,
// гарантирует, что ВСЕ вычисления workspace digest (checks, pipeline candidate,
// delivery planner/executor) используют идентичный ignore-набор. Канонический
// baseline никогда не удаляется — extra только добавляет имена каталогов.
var (
	extraMu         sync.RWMutex
	extraIgnoreDirs = map[string]bool{}
)

// SetExtraIgnoreDirs задаёт project-specific имена каталогов, добавляемые к
// baseline DefaultIgnoreDirs при workspace tree hashing. Вызывается однократно
// при загрузке строго валидированного конфига (см. pkg/config.TreeHash).
func SetExtraIgnoreDirs(names []string) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extraIgnoreDirs = make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			extraIgnoreDirs[name] = true
		}
	}
}

// ResetExtraIgnoreDirs очищает дополнительный набор (тесты / пере-конфиг).
func ResetExtraIgnoreDirs() {
	extraMu.Lock()
	defer extraMu.Unlock()
	extraIgnoreDirs = map[string]bool{}
}

func loadExtraIgnoreDirs() map[string]bool {
	extraMu.RLock()
	defer extraMu.RUnlock()
	// вернуть копию, чтобы вызывающий не мутировал глобальное состояние
	out := make(map[string]bool, len(extraIgnoreDirs))
	for name := range extraIgnoreDirs {
		out[name] = true
	}
	return out
}

// ValidIgnoreDirName — строгий шаблон имени каталога для tree-hash ignore
// (OPS-2). Только простое имя (матчится по имени компонента на любой глубине):
// непустое, без '/', не '.'/'..', не начинается с '-', ограниченной длины.
// Полные пути/glob'ы запрещены — это не ослабляет baseline identity.
func ValidIgnoreDirName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if len(name) > 128 {
		return false
	}
	if name[0] == '-' || name[0] == '/' {
		return false
	}
	for _, r := range name {
		switch {
		case r == '/', r == '\\', r == 0, r == ' ':
			return false
		case r != '_' && r != '-' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9'):
			return false
		}
	}
	return true
}
