# Budget guard + usage truthfulness (P1-7)

## Proposal

**ID**: P1-7  |  **Trace**: #42 budget/usage envelope, ADP-1/ADP-2
**Priority**: P1

### ЧТО

Ограничить каждый run жёстким бюджетом (wall-time + attempts),
всегда, и принимать tokens/cost только от аттестованного adapter-usage.

### ПОЧЕМУ

- Без wall-time/attempt лимитов run в loopback/retry не имеет верхней
  границы (мы это уже видели на практике).
- Usage в envelope Честность: не придумывать цифры — только capability
  `usage-reported` + парсинг вывода агента.

### Скоуп

1. Config: `budget.max_wall_time` (default 24h), `budget.max_attempts`
   (default 100), валидация.
2. Pipeline: wall-time ctx + attempt cap (до запуска лишней попытки),
   break-reason в сообщении.
3. Runtime: optional `UsageReporter`; `AgentCLIRuntime` разбирает usage из
   вывода при успехе (только attested).
4. Metrics: envelope несёт attested usage; `tokens_unknown` только когда
   аттестации нет.

### Критерии приёмки

- Устанавливаемый budget реально останавливает run и называет лимит.
- Без `budget` применяются дефолты (24h / 100).
- Невалидный budget — ошибка конфига.
- Attested usage попадает в usage.json (contextual), un-attested → unknown.

### Похожее / альтернативы

- per-stage timeout остаётся, но это не то же самое (нет общей границы).
- Жёсткие дефолты проще, чем таймаут-классы.