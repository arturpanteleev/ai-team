# Coder не видит будущее ревью — design

## Контекст

Разделение двух механизмов доставки artifacts во входы стадии:

- **Декларативные `inputs`** (`def.yaml`, `stage.go:collectInputs`) — единственный
  способ, которым artifact попадает в prompt/read-контекст стадии на прямом
  проходе.
- **Loopback через `extraInputs`** (`execute.go:152` live-run,
  `pipeline.go:625-632` resume) — единственный способ, которым вердикт-артефакт
  (e.g. `review`) попадает на ПОВТОРНЫЙ прогон source-стадии.

Рантайм-слой: opencode adapter строит read-правила с blanket `deny .ai-team/**`
и разрешает к чтению только явно переданные `inputs`
(`pkg/runtime/opencode.go:89-107`).

## D1: Contract test на pipeline-уровне (основной)

Тест в `pkg/pipeline/pipeline_test.go`:

1. Граф `analyst → coder → tester → reviewer → deployer` (прямой forward-проход,
   без loopback).
2. Через `scriptedRuntime.onExec` собрать имена входов coder на первом (и
   единственном) прогоне coder.
3. Ассерт: набор входов coder == ровно его декларированные `inputs`
   (`proposal` в testRegistry), и НЕ содержит `review`, `report`, `verification`.
4. Также passed a reviewer, чтобы reviewer выводил `review.md` (артефакт
   физически существует), и доказать, что при отсутствии loopback он не доходит
   до coder — т.е. утечка не зависит от наличия файла, а от декларации.

## D2: Подтверждение, что loopback — единственный канал

Положительный сценарий уже покрыт (`TestRun_Loopback_RetryWithReviewInput`,
`TestRun_NonInteractiveReviewLoopbackDecisionResume`). Контракт дополнительно
ассертит: во входах coder на прямом проходе `review` отсутствует, а после
loopback — присутствует. Это пары в одном тесте показывает бинарность канала:
нет loopback → нет review; есть loopback → есть review.

## D3: Runtime-уровень (эшелон защиты)

Уже есть `TestOpenCodeIsolationDeniesEffectsAndNarrowsEdits`, ассертящий
`readRules[".ai-team/**"] == "deny"` и что единственные разрешённые reads —
переданные inputs. Контракт ссылается на него как на второй эшелон. Дополнительно
никакого нового теста не требуется (уже покрыто); упомянем в спеке как
обязательство.

## D4: Нет production-изменений

P1-9 — чисто test-only. Логика уже корректна; цель — закрепить контракт, чтобы
будущие изменения (добавление `review` в inputs coder, автоматическое
подмешивание artifacts в `collectInputs`, расширение read-scope без deny
`.ai-team/**`) ловились регрессионно.

## Отказ от альтернатив

- **Единый snapshot-тест на уровне collectInputs в изоляции** — слабее: не
  проверяет полный цukл stage (collectInputs → snapshot → runtime → outputs).
  Интеграционный тест через `runPipeline` реальнее отражает инвариант.
- **Мокать runtime и проверять только prompt-строку** — дублирует
  `agentcli.buildPrompt`; интереснее проверить именно набор Artifact, которые
  реально передаются в Execute.
