# Fail-closed evidence verification on resume (OPS-3)

## Why

При `--resume` контроллер доверяет сохранённым evidence активного run'а без
явной повторной проверки применимой цепочки/снимков. Повреждение или подмена
`run.json`/`config.json`/`workflow.json`/`events.jsonl`/attempt manifest'ов
между прерыванием и продолжением могло остаться незамеченным (итоговая
проверка anchor происходит только на терминальном run). Решение RQ12 «да» уже
принято — задача чисто исполнительская: перед продолжением выполнять
fail-closed проверку применимой evidence chain/snapshots и явно фиксировать
причину отказа.

## What Changes

- **`pkg/evidence`**:
  - `VerifyResumeEvidence(runDir)` — детерминированная fail-closed проверка
    активного (не-терминального) run'а: manifest identity/schema, digest
    config/workflow snapshot'ов против manifest, целостность event hash chain,
    digest attempt manifest'ов (по replayed chain). Возвращает структурированную
    причину через `ResumeEvidenceError{Reason, Detail}` + сентинель
    `ErrResumeEvidence`.
  - Причины (стабильные коды): `manifest_identity`, `config_snapshot`,
    `workflow_snapshot`, `event_chain`, `attempt_manifest`, `already_terminal`.
  - `AppendBlockedEvent(runDir, runID, reason, detail)` — записывает событие
    `resume_blocked` с причиной, если event log аппендабелен (цепочка цела).
- **`pkg/pipeline`**: при `ResumeRunID` перед `evidence.Resume` вызывается
  `VerifyResumeEvidence`; при отказе (кроме сломанной цепочки/identity, где лог
  неаппендабелен) — фиксируется `resume_blocked` событие, resume отклоняется.

## Acceptance Criteria

1. Fail-closed: повреждение `config.json` (или `workflow.json`, или
   `events.jsonl`, или attempt manifest, или `run.json`) активного run'а →
   `--resume` отклоняется с категоризированной причиной.
2. Причина явно фиксируется в evidence: событие `resume_blocked` с полем
   `reason` = стабильный код (при аппендабельном логе).
3. Чистый resume без tamper'а работает как раньше (регрессия нет).
4. Терминальный run отклоняется причиной `already_terminal`.
5. `make specs` 66/66, `go test ./pkg/evidence ./pkg/pipeline` зелёные.
