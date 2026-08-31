# Signed bundle через DSSE — design

## Контекст: что на самом деле подписываем

- **Run bundle** (`pkg/export`): `export.BundleDigest(index)` —
  sha256(канонический index.json). index.json на диске — `MarshalIndent`+`\n`,
  но digest считается от **compact** `json.Marshal` (детерминирован).
- **Gate bundle** (`pkg/gate`): `gate.BundleDigest(index)` — тот же паттерн.
- **Дизест attestation** (`attest.Digest`) уже встроен в Git trailer
  `ai-team-attestation` (delivery_deferred.go). Это отдельная ценность; P1-5
  фокусируется на bundle-подписи, attestation-трейл остаётся как есть.

Подписываемый payload — **canonical digital-identity bundle digest**
(строка hex). Это детерминированно, компактно и не зависит от layout bundle.

## D1: DSSE envelope без внешних зависимостей

`pkg/dsse` (пакет-per-concern, зеркалит `pkg/attest`/`pkg/export`):

- `PAE(payloadType, payload []byte) []byte` — стандартный DSSE
  Pre-Authentication Encoding: `"DSSEv1" || len(payloadType) || payloadType ||
  len(payload) || payload` (длины — big-endian uint64).
- `Payload{Type string, Bytes []byte}`; `Envelope{PayloadCiphertext/PAE,
  Signatures []Signature}` не обязателен полным: для одного ed25519-подписанта
  достаточно `Peer{KeyID, Sig}` + наша структура envelope.
- `Sign(priv ed25519.PrivateKey, pt string, payload []byte) ([]byte, error)` —
  ed25519.Sign над PAE(pt, payload).
- `Verify(pub ed25519.PublicKey, pt string, payload []byte, sig []byte) error` —
  ed25519.Verify над той же PAE; константо-временное сравнение через
  `crypto/subtle.ConstantTimeCompare`/`ed25519.Verify` (stdlib уже constant-time).
- `LoadPrivateKey(path)`, `LoadPublicKey(path)` — через
  `safeio.ReadRegularFile` + `pem.Decode` (PKCS8/PKCS1 ed25519) или raw ED25519
  seed/лежащий ключ; формат — простой предпочтительный: PEM `PRIVATE KEY`/
  `PUBLIC KEY`, а также fallback на raw base64/PEM-less для простоты. Решаем:
  поддерживаем PEM (PKCS8 ed25519) как канонический, при отсутствии PEM — raw
  32/64-байтный.

Примечание: `ed25519.Sign` в Go всегда возвращает фиксированные 64 байта
(детерминированно для ed25519 — w.r.t. RFC 8032 ed25519 is deterministic). Это
позволяет при желании пересчитать signature для сверки.

## D2: Интеграция подписи в export/gate bundle

Чистый seam — в точке, где bundle уже собран и digest известен:

- **export.Build → return index**; добавить подпись отдельной функцией после:
  `export.SignBundle(index, outDir, priv) error` пишет `dsse.json`
  (envelope с payloadType `"application/vnd.ai-team.bundle+hex"`, payload =
  `BundleDigest(index)`), и НЕ добавляет его в index.Records (иначе частичный:
  digest сменится). Вместо этого `dsse.json` — отдельный файл без записи в
  index; но тогда он «лишний файл» в bundle и verify может не заметить
  подмену. Решение: verify читает `dsse.json` как наличие-необязательное;
  если файл присутствует — он обязан валидироваться против `BundleDigest`, и
  не обязан быть в index.Records (чтобы не менять детерминизм), но дополнительно
  проверяется его целостность напрямую (re-read + parse).
- **VerifyBundle добавка**: `VerifyBundle(bundleDir, opts)` где opts
  содержит `VerifyKey ed25519.PublicKey`. Если `dsse.json` отсутствует и ключа
  нет — как раньше. Если ключ задан (или dsse.json есть) — проверить подпись
  над `BundleDigest(index)` fail-closed. Возвращаем digest как раньше.

Фактически: чтобы не ломать сигнатуру существующих вызовов, вводим
`VerifyOptions{VerifyKey ed25519.PublicKey}` и `VerifyBundle(bundleDir,
opts ...VerifyOptions)` (variadic strings, backward compat). То же для export:
`VerifyBundle(bundleDir, key)`.

## D3: CLI

- `cmdExport`/`cmdGate`: `--sign-key <path>` — загрузить ed25519 confident key и
  подписать bundle после записи.
- `cmdVerify`: `--verify-key <path>` — загрузить публичный ключ и в fail-closed
  режиме потребовать валидную подпись для каждого проверяемого bundle.
- Оба подписывают только BundleDigest; ключ из файла (не из env в prod-коде).

## D4: Backward compat + строгая семантика

- Bundle без `dsse.json` и без `--verify-key` — валиден (как раньше, integrity).
- Bundle с `dsse.json`, verify без ключа — предупредить? Решаем: verify без
  ключа при наличии dsse.json не падает (нельзя проверить), но может вывести
  warning. При заданном `--verify-key` и отсутствии dsse.json → FAIL (мы задали
  ожидание подписи).
- Сигнатура подписи детерминирована (ed25519), потому signature можно
  пересчитать и сверить — включаем в тесты.

## Отказ от альтернатив

- **Внешняя библиотека (in-toto-golang / go-securesystemslib)** — избыточна для
  одного ed25519-подписанта; PAE тривиален на stdlib; сохраняем lean deps.
- **Подпись attestation.Predicate (расширение схемы)** — ломает golden fixture и
  contract-compat statement; не трогаем. Attestation уже связан с Git через
  trailer digest.
- **Подпись всего raw payload (index.json bytes)** — подписываем детерминированный
  digest, а не layout-зависимые байты (Microsoft и gate проще стыковать).

## Риски

- Verify должен сверять dsse.json с digest пересчитываемым из текущего index;
  если кто-то подменит index + dsse.json согласованно, но ключ не тот — fake
  ловится только ключом. Это ожидаемо (подпись = trust in key).
