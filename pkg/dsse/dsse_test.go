package dsse

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestPAE(t *testing.T) {
	// Fixed vectors mirror the DSSE spec example for a short payload:
	// payloadType "application/vnd.test" payload []byte("hello").
	p1 := PAE("application/vnd.test", []byte("hello"))
	if !bytes.HasPrefix(p1, []byte("DSSEv1")) {
		t.Fatalf("PAE не начинается с DSSEv1")
	}
	// len("application/vnd.test") = 20
	// len("hello") = 5
	wantLen := len("DSSEv1") + 8 + 20 + 8 + 5
	if len(p1) != wantLen {
		t.Fatalf("PAE len = %d, want %d", len(p1), wantLen)
	}
	// Determinism.
	if !bytes.Equal(PAE("application/vnd.test", []byte("hello")), p1) {
		t.Fatalf("PAE не детерминирован")
	}
	// Empty payload: длина явно закодирована (0), отлична от непустого.
	empty := PAE("t", nil)
	if len(empty) != len("DSSEv1")+8+1+8+0 {
		t.Fatalf("empty PAE len = %d", len(empty))
	}
	if bytes.Equal(empty, PAE("t", []byte("x"))) {
		t.Fatalf("empty и non-empty PAE одинаковы (не должно)")
	}
}

func TestSignVerifyHappy(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pt := "application/vnd.ai-team.bundle+hex"
	payload := []byte("deadbeef")
	sig := Sign(priv, pt, payload)
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("sig len %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if err := Verify(pub, pt, payload, sig); err != nil {
		t.Fatalf("verify happy: %v", err)
	}
	// Envelope roundtrip.
	env := SignEnvelope(priv, pt, payload)
	if err := VerifyEnvelope(pub, env); err != nil {
		t.Fatalf("verify envelope: %v", err)
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pt := "application/vnd.ai-team.bundle+hex"
	payload := []byte("digest-A")
	sig := Sign(priv, pt, payload)
	if err := Verify(pub, pt, []byte("digest-B"), sig); err == nil {
		t.Fatalf("tampered payload должен FAIL")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("x")
	sig := Sign(priv, "t", payload)
	if err := Verify(pub2, "t", payload, sig); err == nil {
		t.Fatalf("wrong key должен FAIL")
	}
}

func TestVerifyWrongPayloadType(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("x")
	sig := Sign(priv, "type-A", payload)
	if err := Verify(pub, "type-B", payload, sig); err == nil {
		t.Fatalf("wrong payloadType должен FAIL")
	}
}

func TestKeysPEMRoundtrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	privPath := filepath.Join(dir, "ed25519.pem")
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		t.Fatal(err)
	}
	loadedPriv, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatalf("load private: %v", err)
	}
	if !loadedPriv.Equal(priv) {
		t.Fatalf("private key mismatch")
	}

	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	pubPath := filepath.Join(dir, "ed25519.pub.pem")
	if err := os.WriteFile(pubPath, pubPEM, 0644); err != nil {
		t.Fatal(err)
	}
	loadedPub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("load public: %v", err)
	}
	if !loadedPub.Equal(pub) {
		t.Fatalf("public key mismatch")
	}
}

func TestKeysRaw(t *testing.T) {
	dir := t.TempDir()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	privPath := filepath.Join(dir, "key.raw")
	if err := os.WriteFile(privPath, seed, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatalf("raw seed load: %v", err)
	}
	if !loaded.Equal(priv) {
		t.Fatalf("raw seed key mismatch")
	}

	pubPath := filepath.Join(dir, "key.pub")
	if err := os.WriteFile(pubPath, priv.Public().(ed25519.PublicKey), 0644); err != nil {
		t.Fatal(err)
	}
	loadedPub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("raw public load: %v", err)
	}
	if !loadedPub.Equal(priv.Public()) {
		t.Fatalf("raw public key mismatch")
	}
}
