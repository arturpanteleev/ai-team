package approval

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// KeyPathEnv — путь к файлу с MAC-ключом контроллера. Переменная не входит в
// allow-list окружения harness-субпроцесса (pkg/runtime), поэтому даже имя
// файла не доезжает до агента вместе с его env.
const KeyPathEnv = "AI_TEAM_APPROVAL_KEY"

const (
	// MACAlgorithm — единственный поддерживаемый алгоритм аутентификации
	// записи. Явное поле в файле нужно, чтобы смена алгоритма была отказом
	// на чтении, а не молчаливым принятием чужого формата.
	MACAlgorithm = "hmac-sha256"

	keyDirName      = "secrets"
	keyFileName     = "approval.key"
	keySize         = 32
	maxKeyFileBytes = 1 << 10

	// macDomain разделяет области применения ключа: MAC записи approval
	// нельзя переиспользовать как MAC чего-либо ещё, подписанного тем же
	// секретом.
	macDomain = "ai-team/approval-record/v1\x00"
	// keyIDDomain отделяет публичный идентификатор ключа от самих MAC:
	// key_id лежит в открытом файле рядом с подписью и не должен давать
	// ни одного бита, полезного для подделки.
	keyIDDomain = "ai-team/approval-key-id/v1\x00"
)

// ErrIntegrity — сентинель: запись approval не аутентифицирована ключом
// контроллера. Любое его появление означает решение, записанное мимо
// контроллера, и обязано быть fail-closed отказом.
var ErrIntegrity = errors.New("целостность approval нарушена")

// RecordIntegrity — MAC записи approval. Решение человека — единственное, что
// отделяет агента от commit/push/PR, поэтому запись обязана быть
// аутентифицирована секретом, которого у агента нет: сам по себе JSON в
// target'е подделывает любой, кто умеет писать в файловую систему.
type RecordIntegrity struct {
	Alg   string `json:"alg"`
	KeyID string `json:"key_id"`
	MAC   string `json:"mac"`
}

// macKey — загруженный ключ контроллера вместе с публичным идентификатором,
// по которому отличается «подписано другим ключом» от «подделано».
type macKey struct {
	secret []byte
	id     string
	path   string
}

// resolveKeyPath выбирает файл ключа. Ключ обязан лежать вне target: именно в
// target пишет агент, и секрет, доступный ему на запись, не отличим от
// отсутствия секрета.
func resolveKeyPath(target string) (string, error) {
	configured := strings.TrimSpace(os.Getenv(KeyPathEnv))
	var path string
	if configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("approval key path: %w", err)
		}
		path = absolute
	} else {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("approval key: каталог конфигурации пользователя недоступен, укажите %s: %w", KeyPathEnv, err)
		}
		path = filepath.Join(configDir, "ai-team", keyDirName, keyFileName)
	}
	if within, err := insideTarget(target, path); err != nil {
		return "", err
	} else if within {
		return "", fmt.Errorf("approval key %s лежит внутри target %s: ключ контроллера обязан быть недоступен агенту на запись", path, target)
	}
	return path, nil
}

// insideTarget сравнивает пути после разыменования symlink'ов, чтобы
// подстановка ссылки не превращала «внутри target» в «снаружи».
func insideTarget(target, path string) (bool, error) {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(targetAbs); resolveErr == nil {
		targetAbs = resolved
	}
	pathAbs := filepath.Clean(path)
	// Разыменовывается существующий префикс: сам файл ключа может ещё не
	// существовать, а его каталог — уже быть symlink'ом.
	if resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(pathAbs)); resolveErr == nil {
		pathAbs = filepath.Join(resolved, filepath.Base(pathAbs))
	}
	return pathAbs == targetAbs || strings.HasPrefix(pathAbs, targetAbs+string(filepath.Separator)), nil
}

// loadOrCreateKey читает ключ, а при его отсутствии создаёт новый строго через
// O_EXCL: параллельный контроллер не должен получить второй ключ, иначе часть
// уже подписанных записей стала бы «повреждённой».
func loadOrCreateKey(path string) (macKey, error) {
	data, err := safeio.ReadRegularFile(path, maxKeyFileBytes)
	if os.IsNotExist(err) {
		created, createErr := createKeyFile(path)
		if createErr != nil {
			return macKey{}, createErr
		}
		data = created
	} else if err != nil {
		return macKey{}, fmt.Errorf("approval key %s: %w", path, err)
	}
	if err := checkKeyFileAccess(path); err != nil {
		return macKey{}, err
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(secret) != keySize {
		return macKey{}, fmt.Errorf("approval key %s повреждён: ожидались %d hex-байт", path, keySize)
	}
	return macKey{secret: secret, id: keyID(secret), path: path}, nil
}

func createKeyFile(path string) ([]byte, error) {
	if err := safeio.EnsureDirPath(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("approval key dir: %w", err)
	}
	// Каталог с секретом закрывается от остальных пользователей независимо от
	// umask: права по умолчанию у MkdirAll были бы 0755.
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("approval key dir: %w", err)
	}
	secret := make([]byte, keySize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("approval key entropy: %w", err)
	}
	encoded := []byte(hex.EncodeToString(secret) + "\n")
	// 0400: перезапись ключа — не штатная операция, а атака или ошибка
	// оператора; read-only файл делает её заметной.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		if os.IsExist(err) {
			// Гонка с другим контроллером: выигравший ключ и есть общий.
			return safeio.ReadRegularFile(path, maxKeyFileBytes)
		}
		return nil, fmt.Errorf("approval key %s: %w", path, err)
	}
	// Недописанный ключ удаляется: иначе O_EXCL при следующем запуске увидел
	// бы «существующий» ключ и каждый старт падал бы на повреждённом файле.
	// Ошибка Close на этом пути ничего не добавляет к уже случившемуся сбою.
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func keyID(secret []byte) string {
	sum := sha256.Sum256(append([]byte(keyIDDomain), secret...))
	return hex.EncodeToString(sum[:8])
}

// mac аутентифицирует канонический вид записи. Само поле integrity в вход не
// входит — иначе MAC зависел бы от самого себя.
func (k macKey) mac(value PendingApproval) (string, error) {
	canonical, err := canonicalRecord(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, k.secret)
	mac.Write([]byte(macDomain))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// canonicalRecord — байты записи, которые аутентифицирует MAC. Payload
// компактизируется, потому что на диск он уходит переиндентированным
// (MarshalIndent) и после round-trip не совпал бы с исходными байтами.
func canonicalRecord(value PendingApproval) ([]byte, error) {
	value.Integrity = nil
	if len(value.Payload) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, value.Payload); err != nil {
			return nil, fmt.Errorf("approval payload: %w", err)
		}
		value.Payload = compact.Bytes()
	}
	return json.Marshal(value)
}

// sign возвращает запись с проставленным MAC контроллера.
func (k macKey) sign(value PendingApproval) (PendingApproval, error) {
	mac, err := k.mac(value)
	if err != nil {
		return PendingApproval{}, err
	}
	value.Integrity = &RecordIntegrity{Alg: MACAlgorithm, KeyID: k.id, MAC: mac}
	return value, nil
}

// verify — fail-closed проверка записи, прочитанной с диска.
func (k macKey) verify(value PendingApproval) error {
	if value.Integrity == nil {
		// Запись предыдущей схемы отличается от подделки только происхождением,
		// но принять её нельзя ни в том, ни в другом случае: оператор должен
		// понимать, что делать, поэтому причины названы раздельно.
		if value.SchemaVersion > 0 && value.SchemaVersion < SchemaVersion {
			return fmt.Errorf("%w: запись %s сохранена схемой %d, где MAC контроллера ещё не было — решение нужно запросить заново",
				ErrIntegrity, value.ID, value.SchemaVersion)
		}
		return fmt.Errorf("%w: запись %s не подписана контроллером — решение записано мимо ai-team", ErrIntegrity, value.ID)
	}
	if value.Integrity.Alg != MACAlgorithm {
		return fmt.Errorf("%w: запись %s подписана алгоритмом %q вместо %s", ErrIntegrity, value.ID, value.Integrity.Alg, MACAlgorithm)
	}
	expected, err := k.mac(value)
	if err != nil {
		return err
	}
	actual, decodeErr := hex.DecodeString(value.Integrity.MAC)
	expectedRaw, _ := hex.DecodeString(expected)
	if decodeErr != nil || !hmac.Equal(expectedRaw, actual) {
		if value.Integrity.KeyID != k.id {
			return fmt.Errorf("%w: запись %s подписана ключом %s, контроллер использует %s (%s)",
				ErrIntegrity, value.ID, value.Integrity.KeyID, k.id, k.path)
		}
		return fmt.Errorf("%w: MAC записи %s не совпадает — решение изменено на диске после записи контроллером",
			ErrIntegrity, value.ID)
	}
	return nil
}
