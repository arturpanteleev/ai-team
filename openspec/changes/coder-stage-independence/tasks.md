# Coder не видит будущее ревью — tasks

## T1: Negative contract test (forward pass)

В `pkg/pipeline/pipeline_test.go` добавить `TestForwardPass_CoderDoesNotSeeFutureReview`:

- Граф `analyst → coder → tester → reviewer → deployer` (без loopback).
- `scriptedRuntime.onExec`: собирать имена входов coder (первый и единственный
  прогон coder).
- Дальше reviewer пишет `review.md` и выводит APPROVED (чтобы граф двигался
  вперёд).
- Ассерты:
  - coder вызван ровно 1 раз.
  - во входах coder нет ни `review`, ни `report`, ни `verification`.
  - входа ровно один: `proposal` (декларированный в testRegistry).
  - `review.md` физически существует в artifact root (доказательство, что
    отсутствие во входах — это декларация, а не отсутствие файла).

## T2: Binary-channel pair (loopback adds review)

В том же тесте (или в существующем `TestRun_Loopback_RetryWithReviewInput`)
закрепить пару:
- forward coder inputs не содержат `review`;
- после loopback retry coder inputs содержат `review`.

Если T1 планирует forward-only граф, достаточно добавить в существующий
loopback-тест ассерт на ПЕРВЫЙ (forward) прогон coder: `review` отсутствует.
Это даёт бинарность в одном сценарии: нет loopback → нет review; есть → есть.

## T3: Runtime isolation тест (эшелон)

Убедиться, что `TestOpenCodeIsolationDeniesEffectsAndNarrowsEdits` уже ассертит
`.ai-team/** = deny` и только inputs в read scope (probe). Если да — явно
закрепить как часть контракта (закомментировать ссылкой). Никакого нового
production-кода.

## T4: Docs + status

- `TASKLOG.md`: новая строка P1-9 #19 → review; open 31 / review 16.
- `ai-team-backlog.html`: #19 status Open → Review (tag Independence).
- `openspec/changes/coder-stage-independence/{proposal,design,spec,tasks}`.
- `make specs` зелёный.
