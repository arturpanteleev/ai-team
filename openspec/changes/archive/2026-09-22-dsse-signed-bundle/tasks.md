# Tasks — Signed bundle через DSSE

## D1. pkg/dsse (DSSE envelope, stdlib-only)

- [x] `pkg/dsse/dsse.go`: `PAE(payloadType, payload)` — `"DSSEv1"` + big-endian
      uint64 lengths + payloadType + payload; `Envelope{PayloadType, Payload,
      Signature}`; `Sign(priv, pt, payload) ([]byte, error)` — ed25519.Sign over
      PAE; `Verify(pub, pt, payload, sig) error` — ed25519.Verify.
- [x] `pkg/dsse/keys.go`: `LoadPrivateKey(path)` (PEM PKCS8 ed25519; fallback raw
      64-byte/seed), `LoadPublicKey(path)` (PEM PKCS8/PKIX; fallback raw
      32-byte) — через `safeio.ReadRegularFile` + `pem.Decode`.
- [x] `pkg/dsse/dsse_test.go`: PAE roundtrip + canon, sign/verify happy,
      tampered payload FAIL, wrong key FAIL, key load PEM+raw.

## D2. Подпись + верификация в export/gate

- [x] `pkg/export/export.go`: `SignBundle(bundleDir, priv) error` writes
      `dsse.json` (envelope, payload=`BundleDigest(index)`); `VerifyBundle`
      gains variadic `VerifyOptions{VerifyKey}` — if key set/dsse.json present,
      verify fail-closed over `BundleDigest`.
- [x] `pkg/gate/gate.go`: аналогично `SignBundle` + `VerifyBundle` variadic key.
- [x] Реэкспорт `dsse.json` не в index.Records (не ломать детерминизм) — verify
      читает его отдельно.

## D3. CLI

- [x] `cmd/ai-team/export.go`, `cmd/gate.go`: `--sign-key <path>` load key &
      `SignBundle` after write.
- [x] `cmd/ai-team/main.go` cmdVerify: `--verify-key <path>`, wire into
      export/gate VerifyBundle; gate/run dir branches.

## D4. Тесты + доки

- [x] export/gate unit: signed bundle roundtrip, tamper fail, missing sig with
      key fail, unsigned-no-key pass.
- [x] README «integrity vs authenticity» обновить (заголовок сохранить);
      ARCHITECTURE секция.
- [x] `TASKLOG.md` #20→review; backlog #20→Review. (Файл `TASKLOG.md`
      удалён позднее в 6aa173a при подготовке публичного репозитория —
      пункт неактуален.)
- [x] `make specs` (64+); `go test ./...` (кроме baseline e2e).
