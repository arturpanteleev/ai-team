package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const localWebTokenRelativePath = ".ai-team/web.token"

func printLocalWebToken(out io.Writer, token, path string) error {
	_, err := fmt.Fprintf(out, "Локальный токен web-интерфейса: %s\nФайл токена: %s\n", token, path)
	return err
}

func loadOrCreateLocalWebToken(target string) (string, string, error) {
	controlDir, err := safeio.EnsureDir(target, ".ai-team")
	if err != nil {
		return "", "", fmt.Errorf("local web token directory: %w", err)
	}
	path := filepath.Join(controlDir, "web.token")
	data, readErr := safeio.ReadRegularFile(path, 1024)
	if readErr == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return "", "", fmt.Errorf("local web token metadata: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("local web token must be a regular file without symlink")
		}
		if info.Mode().Perm() != 0o600 {
			return "", "", fmt.Errorf("local web token permissions are %04o; expected 0600", info.Mode().Perm())
		}
		token := strings.TrimSuffix(string(data), "\n")
		if err := validateLocalWebToken(token); err != nil {
			return "", "", fmt.Errorf("invalid local web token file: %w", err)
		}
		return token, path, nil
	}
	if !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("read local web token: %w", readErr)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate local web token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := safeio.WriteRegularFileNoFollow(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("write local web token: %w", err)
	}
	return token, path, nil
}

func validateLocalWebToken(token string) error {
	if len(token) != 43 || strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("expected a 32-byte base64url token")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("expected a 32-byte base64url token")
	}
	return nil
}
