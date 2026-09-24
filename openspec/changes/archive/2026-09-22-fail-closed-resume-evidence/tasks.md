# Tasks — Fail-closed evidence verification on resume (OPS-3)

## T1. pkg/evidence: явный fail-closed гейт

- [x] `pkg/evidence/resumeverify.go`:
  - сентинель `ErrResumeEvidence`, тип `ResumeEvidenceError{Reason, Detail}`
    (errors.As/Is), стабильные коды `ResumeEvidenceReason`
    (`manifest_identity`, `config_snapshot`, `workflow_snapshot`,
    `event_chain`, `attempt_manifest`, `already_terminal`).
  - `VerifyResumeEvidence(runDir)` — fail-fast проверка: run.json
    (schema/run_id), config/workflow snapshot digests, ReplayEventLog (chain +
    terminality), attempt manifest digests.
  - `AppendBlockedEvent(runDir, runID, reason, detail)` +
    `openStoreForAppend` — запись `resume_blocked` при аппендабельной chain.

## T2. pkg/evidence: тесты

- [x] `resumeverify_test.go`: активный run проходит; config tamper →
      `config_snapshot`; event chain tamper → `event_chain`; terminal →
      `already_terminal` + sentinel unwrap.

## T3. pkg/pipeline: интеграция

- [x] resume-ветка `RunWithResult`: `VerifyResumeEvidence` ДО `evidence.Resume`;
      при отказе (не chain/identity) `AppendBlockedEvent`; возврат
      категоризированной ошибки.
- [x] `resume_evidence_test.go`: повреждение config активного run'а →
      resume отклоняется + `resume_blocked` записан с reason=`config_snapshot`,
      chain остаётся валидной.

## T4. Verification

- [x] `go build ./...`, `go vet`, `gofmt -l` чисто.
- [x] `go test ./pkg/evidence ./pkg/pipeline` зелёные;
      `make specs` 66/66.
