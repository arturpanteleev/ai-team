// Package dsse implements a minimal DSSE (in-toto-style) signature envelope
// using only the standard library: Pre-Authentication Encoding (PAE) plus
// ed25519 sign/verify. Used to bind authenticity to the deterministic
// BundleDigest of run- and gate-bundles (P1-5). No external dependencies —
// matches the project's lean, stdlib-first posture.
package dsse

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Pre-Authentication Encoding prefix per DSSE spec.
const paePrefix = "DSSEv1"

var (
	// ErrVerify — подпись не прошла проверку (подмена payload или ключ не тот).
	ErrVerify = errors.New("dsse: подпись не совпадает")
)

// Envelope — сериализованный DSSE envelope над одним payload.
// PayloadType — media type payload'а; Payload — подписываемые байты;
// Signature — ed25519-подпись над PAE(payloadType, payload) (64 байта).
// Для детерминированности (ed25519 детерминирован по RFC 8032) signature
// можно пересчитать и сверить. KeyID/KeyHash не храним — подпись и ключ
// связываются только выбором verify-ключа вызывающей стороной.
type Envelope struct {
	PayloadType string `json:"payloadType"`
	Payload     []byte `json:"payload"`
	Signature   []byte `json:"signature"`
}

// PAE возвращает каноническое Pre-Authentication Encoding окрытие payload'а:
//
//	DSSEv1 || len(payloadType, uint64 BE) || payloadType ||
//	len(payload, uint64 BE) || payload
//
// Одинаковые (payloadType, payload) всегда дают одинаковые байты.
func PAE(payloadType string, payload []byte) []byte {
	var buf []byte
	buf = append(buf, paePrefix...)
	buf = appendUint64(buf, uint64(len(payloadType)))
	buf = append(buf, payloadType...)
	buf = appendUint64(buf, uint64(len(payload)))
	buf = append(buf, payload...)
	return buf
}

func appendUint64(dst []byte, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	return append(dst, tmp[:]...)
}

// Signinal подписывает payload ed25519-ключом priv над PAE(payloadType, payload).
// Возвращает фиксированную 64-байтную подпись.
func Sign(priv ed25519.PrivateKey, payloadType string, payload []byte) []byte {
	return ed25519.Sign(priv, PAE(payloadType, payload))
}

// Verify проверяет подпись по открытому ключу pub над PAE(payloadType, payload).
// Возвращает ErrVerify при несовпадении (подмена payload или неверный ключ).
func Verify(pub ed25519.PublicKey, payloadType string, payload, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: некорректный открытый ключ (%d байт)", ErrVerify, len(pub))
	}
	if !ed25519.Verify(pub, PAE(payloadType, payload), sig) {
		return ErrVerify
	}
	return nil
}

// SignEnvelope подписывает payload и возвращает готовый Envelope.
func SignEnvelope(priv ed25519.PrivateKey, payloadType string, payload []byte) *Envelope {
	return &Envelope{
		PayloadType: payloadType,
		Payload:     payload,
		Signature:   Sign(priv, payloadType, payload),
	}
}

// VerifyEnvelope проверяет envelope по открытому ключу.
func VerifyEnvelope(pub ed25519.PublicKey, e *Envelope) error {
	return Verify(pub, e.PayloadType, e.Payload, e.Signature)
}

// Marshal сериализует envelope в стабильный JSON (для dsse.json в bundle:
// детерминирован, подпись не зависит от layout). delta-поле отсутствует.
func Marshal(e *Envelope) ([]byte, error) {
	return json.MarshalIndent(e, "", "  ")
}

// EnvelopeFileName — имя signature-файла в bundle (рядом с index.json).
const EnvelopeFileName = "dsse.json"

// SignaturePayloadType — media type подписываемого payload'а: детерминированный
// BundleDigest (hex-строка) run- или gate-bundle.
const SignaturePayloadType = "application/vnd.ai-team.bundle+hex"

// SignBundleFile подписывает bundle digest и пишет EnvelopeFileName в bundleDir.
// Errors оставляют каталог без частичного файла при ошибке записи.
func SignBundleFile(bundleDir string, priv ed25519.PrivateKey, payloadType string, payload []byte) error {
	env := SignEnvelope(priv, payloadType, payload)
	data, err := Marshal(env)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(bundleDir, EnvelopeFileName)
	if err := os.WriteFile(path, data, 0444); err != nil {
		return err
	}
	return nil
}

// ReadEnvelopeFile читает EnvelopeFileName из bundleDir (nil, если нет файла).
func ReadEnvelopeFile(bundleDir string) (*Envelope, bool, error) {
	data, err := os.ReadFile(filepath.Join(bundleDir, EnvelopeFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		return nil, false, err
	}
	return &env, true, nil
}
