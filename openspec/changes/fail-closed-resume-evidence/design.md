# Fail-closed evidence verification on resume (OPS-3) — design

## Точка вставки

`evidence.Resume` (store.go) уже проверяет manifest identity/schema, digest
config/workflow snapshot и event hash chain, но: (1) возвращает общим error без
стабильного кода причины; (2) не проверяет attempt manifest digests по
replayed chain отдельным шагом; (3) в `RunEngine.Cancel` и в resume-ветке
`pipeline.RunWithResult` ошибка не фиксируется событием в evidence.

OPS-3 вводит явный fail-closed гейт `VerifyResumeEvidence(runDir)`, вызываемый в
resume-ветке `RunWithResult` ДО `evidence.Resume`. Это даёт единую точку ответа
«можно ли продолжать» с категоризированной причиной, независимо от того, где
именно расхождение (snapshot, цепочка, attempt manifest, терминальность).

## Формат причины

`ResumeEvidenceError{Reason ResumeEvidenceReason, Detail string}` + сентинель
`ErrResumeEvidence` (`errors.Is`/`errors.As` совместимы). Reason — стабильный
машинный код, чтобы потребитель (CLI/web/цикл) мог обработать однозначно и
записать в `resume_blocked` без парсинга строк.

## Проверка (порядок — fail-fast)

1. `run.json` парсится (DisallowUnknownFields), schema + run_id валидны
   (`manifest_identity`).
2. digest `config.json` и `workflow.json` по manifest-полям
   (`config_snapshot` / `workflow_snapshot`).
3. `ReplayEventLog` (валидация hash chain + переходы) (`event_chain`);
   `FinishedAt.IsZero()` обязателен, иначе `already_terminal`.
4. digest attempt manifest'ов по `ManifestSHA256` из replayed chain
   (`attempt_manifest`).

## Запись причины (аппендабельный лог)

`s.resume_blocked` запиcывается только когда лог цел (целостная цепочка
позволяет append через `Store.Append`, который сам верифицирует цепочку).
`AppendBlockedEvent` пере-открывает store по валидной chain
(`openStoreForAppend`) — это независимо от `evidence.Resume`, который мог бы
упасть на snapshot-ошибке. Event type `resume_blocked` — информационный:
`ReplayEventLog` игнорирует незнакомые типы (нет default-error), поэтому новые
events не ломают проекцию прошлого.

Причина в `data`: `{"reason": "<стабильный код>", "detail": "<текст>"}`.

## Отказ от альтернатив

- **Усилить только `evidence.Resume`** — не даёт стабильного кода причины и не
  фиксирует событие; OPS-3 требует «явно фиксировать причину отказа».
- **Полный `VerifyAnchor` на resume** — anchor есть только у терминального run;
  активный run ещё без anchor (backlog это явно отмечает). Отдельный
  non-terminal гейт — правильно.
- **Событие на каждый resume-отказ** — пишем только при целостной цепочке,
  иначе запись сама невозможна; сам отказ уже fail-closed.

## Риски

- `VerifyResumeEvidence` дублирует часть чтений `evidence.Resume` — приемлемо:
  это explicit гейт, читаемый и тестируемый отдельно; cost мал (несколько файлов
  маленького размера).
- Новый event type должен проходить `Store.Append` (валидация цепочки) и не
  ломать `ReplayEventLog` — покрыто тестом (`event_chain` остаётся валидной
  после `resume_blocked`).
