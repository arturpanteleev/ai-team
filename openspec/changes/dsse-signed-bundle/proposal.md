# Signed bundle через DSSE

## Why

README и ARCHITECTURE честно фиксируют разрыв: **integrity** гарантируется
цепочкой хэшей (`ai-team verify` подтверждает, что записи не тронуты), но
**authenticity** — нет: у bundle нет подписи автора. Digest — это контроль
целостности (факт «не изменено»), а не доказательство авторства («кто создал»).
Доверие к создателю — out-of-band решение человека/CI.

P1-5 закрывает этот разрыв: подпись DSSE (in-toto style) на deteterministic
`BundleDigest` run- и gate-bundle, верифицируемая публичным ключом. Это переносит
bundle из «целостность без автора» в «подписано автором» — ключевой шаг к
внешнему audit и к доверию к bundle как самостоятельному артефакту.

## What Changes

- **`pkg/dsse`**: канонический DSSE envelope (PAE — Pre-Authentication Encoding:
  `"DSSEv1" + len(payloadType) + payloadType + len(payload) + payload`) + ed25519
  sign/verify через stdlib `crypto/ed25519`. Ноль внешних зависимостей —
  соответствует lean stdlib-позиции проекта.
- **Подпись run- и gate-bundle**: sign детерминированного `BundleDigest`
  (канонический index.json) в момент финализации bundle.
- **CLI**: ключи через флаги/окружение (без коммита секретов):
  - `ai-team export --sign-key <path>` / `ai-team gate --sign-key <path>` —
    подписать bundle;
  - `ai-team verify --verify-key <path>` — верифицировать подпись (fail-closed:
    если signature-файл есть, а ключ задан и не совпадает → ошибка).
- **Evidence**: signature записывается отдельным файлом bundle
  (`dsse.json` / `.sig`) и перечисляется в index.Records, чтобы она сама
  покрывалась хэш-цепочкой. При верификации signature-файл и digest проверяются
  согласованно.

## Success Criteria

- [green] `pkg/dsse` unit-тесты: PAE roundtrip, sign/verify, tampered signature
  ловится, wrong key ловится (fail-closed).
- [green] export/gate подписывают bundle — signature-файл появляется и попадает
  в index.Records; `ai-team verify --verify-key` проверяет.
- [negative] Подмена payload / подпись другим ключом / отсутствие подписи при
  заданном `--verify-key` → verify FAIL (закрытие authenticity gAP).
- README «integrity vs authenticity» обновлён (заголовок сохранить как строку —
  docs_test.go); AUDIT-статус по подписи.
- `make specs` зелёный; `go test ./...` зелёный (кроме известного baseline e2e).
