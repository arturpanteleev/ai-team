package dsse

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// LoadPrivateKey загружает ed25519 private key из файла. Поддерживает:
//   - PEM PKCS8 ("PRIVATE KEY")
//   - PEM PKCS1 ("RSA PRIVATE KEY") — не применим к ed25519, отклоняется
//   - raw: 64-байтный PKCS8/raw seed 32 байта (fallback)
//
// Путь читается через os.ReadFile (ключ локальный, вне bundle-правил safeio)
// и не должен попадать в evidence.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dsse: читать private key %s: %w", path, err)
	}
	key, err := parsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("dsse: private key %s: %w", path, err)
	}
	return key, nil
}

// LoadPublicKey загружает ed25519 public key из файла (PEM PKCS8/PKIX или raw
// 32-байтный).
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dsse: читать public key %s: %w", path, err)
	}
	key, err := parsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("dsse: public key %s: %w", path, err)
	}
	return key, nil
}

func parsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		if block.Type == "PRIVATE KEY" {
			parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			key, ok := parsed.(ed25519.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("PEM не ed25519 private key (%T)", parsed)
			}
			return key, nil
		}
		return nil, fmt.Errorf("неподдерживаемый PEM block %q", block.Type)
	}
	if len(data) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(data), nil
	}
	if len(data) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(data), nil
	}
	return nil, fmt.Errorf("raw private key должен быть %d (seed) или %d (full) байт, получено %d",
		ed25519.SeedSize, ed25519.PrivateKeySize, len(data))
}

func parsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		var parsed any
		var err error
		switch block.Type {
		case "PRIVATE KEY", "PUBLIC KEY":
			parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
			if err == nil {
				if key, ok := parsed.(ed25519.PrivateKey); ok {
					return key.Public().(ed25519.PublicKey), nil
				}
			}
			// try PKIX public
			pub, pubErr := x509.ParsePKIXPublicKey(block.Bytes)
			if pubErr != nil {
				return nil, errors.Join(err, pubErr)
			}
			key, ok := pub.(ed25519.PublicKey)
			if !ok {
				return nil, fmt.Errorf("не ed25519 public key (%T)", pub)
			}
			return key, nil
		case "ED25519 PRIVATE KEY":
			// openssl genpkey -algorithm ED25519 emits PKCS8, handle direct
			return nil, fmt.Errorf("use PKCS8 'PRIVATE KEY' PEM instead of %q", block.Type)
		default:
			return nil, fmt.Errorf("неподдерживаемый PEM block %q", block.Type)
		}
	}
	if len(data) == ed25519.PublicKeySize {
		return ed25519.PublicKey(data), nil
	}
	if len(data) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(data).Public().(ed25519.PublicKey), nil
	}
	return nil, fmt.Errorf("raw public key должен быть %d байт, получено %d",
		ed25519.PublicKeySize, len(data))
}
