package delivery

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// Delivery state files live in the working tree (.ai-team/delivery), which an
// agent may be able to write. Redelivery stacks a new commit on a branch tip
// only when that tip is proven to be a controller-delivered commit, so every
// state is authenticated with an HMAC-SHA256 whose key lives outside the
// working tree: $AI_TEAM_STATE_KEY_DIR, else <user config dir>/ai-team. The key
// is created with mode 0600 on first use. A state without a valid MAC (written
// by an older version, or forged) is never trusted for redelivery; the planner
// then refuses and a clean branch from base is required.

const (
	stateKeyDirEnv  = "AI_TEAM_STATE_KEY_DIR"
	stateKeyFile    = "delivery-state.key"
	stateMACContext = "ai-team delivery state v1\n"
)

func stateKeyPath() (string, error) {
	dir := strings.TrimSpace(os.Getenv(stateKeyDirEnv))
	if dir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(config, "ai-team")
	}
	return filepath.Join(dir, stateKeyFile), nil
}

// loadStateKey returns the HMAC key. With create=false a missing key is an
// error and nothing is written.
func loadStateKey(create bool) ([]byte, error) {
	path, err := stateKeyPath()
	if err != nil {
		return nil, err
	}
	if data, readErr := safeio.ReadRegularFile(path, 4096); readErr == nil {
		key, decodeErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("delivery state key %s повреждён", path)
		}
		return key, nil
	} else if !errors.Is(readErr, fs.ErrNotExist) || !create {
		return nil, readErr
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return loadStateKey(false)
	}
	if err != nil {
		return nil, err
	}
	_, writeErr := file.WriteString(hex.EncodeToString(key) + "\n")
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return nil, writeErr
	}
	return key, nil
}

func stateMAC(key []byte, value state) string {
	value.MAC = ""
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(stateMACContext))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// signState is best effort: when the key cannot be obtained the state is
// written without a MAC and simply cannot be used to authorise redelivery.
func signState(value *state) string {
	key, err := loadStateKey(true)
	if err != nil {
		return ""
	}
	return stateMAC(key, *value)
}

// verifyStateMAC reports whether the loaded state carries a valid MAC.
func verifyStateMAC(value *state) error {
	if value.MAC == "" {
		return errors.New("delivery state не подписан контроллером")
	}
	key, err := loadStateKey(false)
	if err != nil {
		return fmt.Errorf("ключ подписи delivery state недоступен: %w", err)
	}
	expected, err := hex.DecodeString(stateMAC(key, *value))
	actual, decodeErr := hex.DecodeString(value.MAC)
	if err != nil || decodeErr != nil || !hmac.Equal(expected, actual) {
		return errors.New("подпись delivery state неверна")
	}
	return nil
}
