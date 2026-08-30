// Package risk измеряет risk-signals изменений (V0-8): размер и тип diff,
// чувствительные пути, тестовые изменения, failed checks. Сигналы записываются
// в attestation bundle как данные, но НЕ маршрутизируют pipeline — никакого
// автоматического решения на их основе нет (routing придёт отдельно в
// P2-1/P2-2 вместе с продуктивным решением владельца).
package risk

import (
	"path"
	"sort"
	"strings"
)

// Kind — тип чувствительного пути.
const (
	KindEnv         = "env"         // .env и аналоги: переменные окружения/конфигурация
	KindSecrets     = "secrets"     // ключи, сертификаты, secret-каталоги
	KindCredentials = "credentials" // файлы авторизации/токенов
)

// Entry — один чувствительный путь и его тип.
type Entry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// sensitiveExtensions — расширения секретов/ключей.
var sensitiveExtensions = map[string]string{
	".pem":      KindSecrets,
	".key":      KindSecrets,
	".p12":      KindSecrets,
	".pfx":      KindSecrets,
	".jks":      KindSecrets,
	".keystore": KindSecrets,
	".secret":   KindSecrets,
	".token":    KindSecrets,
	".tok":      KindSecrets,
}

// sensitiveBases — имена файлов, признаваемых чувствительными по имени.
var sensitiveBases = map[string]string{
	".npmrc":                               KindCredentials,
	".pypirc":                              KindCredentials,
	".netrc":                               KindCredentials,
	".htpasswd":                            KindCredentials,
	".aws/credentials":                     KindCredentials,
	".docker/config.json":                  KindCredentials,
	"credentials.json":                     KindCredentials,
	"service-account.json":                 KindCredentials,
	"service_account.json":                 KindCredentials,
	"application_default_credentials.json": KindCredentials,
}

// sensitiveSegments — каталоги, содержимое которых чувствительно.
var sensitiveSegments = map[string]string{
	"secrets":     KindSecrets,
	".ssh":        KindSecrets,
	".gnupg":      KindSecrets,
	".aws":        KindCredentials,
	"credentials": KindCredentials,
}

// isPrivateKeyBase — имена приватных ключей OpenSSH/OpenPGP.
func isPrivateKeyBase(base string) bool {
	switch base {
	case "id_rsa", "id_ed25519", "id_ed448", "id_ecdsa", "id_dsa", "id_ecdsa_sk", "id_ed25519_sk":
		return true
	}
	return false
}

// envKind — тип для файлов .env. .env.example — общепринятый шаблон без
// значений и не считается чувствительным (иначе сигнал превращается в шум).
func envKind(base string) string {
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		if base == ".env.example" || base == ".env.sample" {
			return ""
		}
		return KindEnv
	}
	return ""
}

// Entries возвращает чувствительные пути среди repository-relative путей:
// отсортированные по пути и без дублей; каждый путь ровно с одним типом
// (приоритет: каталог > расширение > basename > env).
func Entries(repoRelative []string) []Entry {
	index := make(map[string]Entry)
	for _, candidate := range repoRelative {
		value := path.Clean(strings.ReplaceAll(candidate, "\\", "/"))
		value = strings.TrimPrefix(value, "./")
		if value == "." || value == "" {
			continue
		}
		base := path.Base(value)
		dir := path.Dir(value)

		entry := Entry{Path: value}
		kind := ""
		for _, segment := range strings.Split(dir, "/") {
			if k, ok := sensitiveSegments[segment]; ok {
				kind = k
				break
			}
		}
		if kind == "" {
			if k, ok := sensitiveExtensions[strings.ToLower(path.Ext(base))]; ok {
				kind = k
			}
		}
		if kind == "" && isPrivateKeyBase(base) {
			kind = KindSecrets
		}
		if kind == "" {
			if k, ok := sensitiveBases[base]; ok {
				kind = k
			}
		}
		if kind == "" {
			kind = envKind(base)
		}
		if kind == "" {
			continue
		}
		entry.Kind = kind
		if existing, ok := index[value]; ok {
			if existing.Kind != kind && kind == KindSecrets {
				index[value] = entry
			}
			continue
		}
		index[value] = entry
	}
	entries := make([]Entry, 0, len(index))
	for _, entry := range index {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}
