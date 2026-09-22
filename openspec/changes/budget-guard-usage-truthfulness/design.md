# Budget guard + usage truthfulness (P1-7)

## Problem

Две смежные проблемы бюджета run'а:

1. **Нет верхней границы**. run может крутиться в loopback/retry бесконечно:
   wall-time ограничен только per-stage timeout-ами, а суммарное число
   попыток не ограничено ничем.
2. **Tokens/cost вычисляются нечестно**. Pipeline ещё не потребляет usage из
   адаптеров; envelope всегда пишет `tokens_unknown: true`, а если начнёт
   принимать цифры — нужно гарантировать, что они приходят только от
   аттестованного источника (capability `usage-reported`), а не guesses.

## Requirements

- Жёсткий бюджет run: `max_wall_time` (default 24h) и `max_attempts`
  (default 100). Применяется ВСЕГДА, даже без секции `budget`.
- Бюджет-брейч — явная причина остановки run (не generic error).
- Usage принимается ТОЛЬКО от адаптера с capability `usage-reported`
  (runtime `UsageSource`), разбор из вывода агента в `AgentCLIRuntime`.
- Envelope содержит attested usage: `usage_reported`,
  `tokens_input/output`, `cost_usd`; `tokens_unknown` остаётся при отсутствии
  аттестации.

## Non-goals

- Не меняем per-stage timeout (stage_timeout) и workflow max_visits.
- Не добавляем UI/документацию по budget-настройке (только конфиг).
- Cost-конвертация между моделями не входит (брать как есть из адаптера).

## Design

### Config

`pkg/config/config.go`: `BudgetConfig` (yaml `budget`, поля `max_wall_time`,
`max_attempts`) с `Validate()` (положительное парсимое duration / неотрицательный
int), `EffectiveMaxWallTime()` и `EffectiveMaxAttempts()` — nil-safety дефолты.
Разрешён в allowlist/rawConfig/`Config.Validate`.

### Runtime: attested usage

- `adapter.go` уже имеет `Usage{TokensInput,TokensOutput,CostUSD,Attested}`,
  `UsageSource.ParseUsage(io.Reader)` (для claude/codex). Это единственная
  точка, где usage может появиться — harness сам цифры не придумывает.
- `runtime.go`: optional interface `UsageReporter { Usage() *Usage }`.
- `agentcli.go`: `AgentCLIRuntime.lastUsage`; в `Execute` ПОСЛЕ успеха, если
  adapter реализует `UsageSource`, парсим stdout и сохраняем usage (только
  `Attested`). На досупе в промпте/adapter usage не зашит.

### Pipeline: лимиты и агрегация

- runState: `budgetConfig` (из `p.cfg.Budget`, nil-safe дефолты) и
  `usageTotal runtime.Usage`.
- `RunWithResult`: execute обёрнут в `context.WithTimeout(ctx, budgetWall)`.
  `errors.Is(runErr, context.DeadlineExceeded)` → причина `max_wall_time`.
- `executeGraph`: ПЕРЕД `runStage` проверка
  `attemptOrdinal >= maxAttempts` → бюджет-ошибка (cap строгий: лишняя
  попытка не запускается).
- `runStage`: после успеха, если runtime реализует `UsageReporter` и usage
  `Attested` — суммирует в `rs.usageTotal`.

### Metrics

`metrics.go`: `Usage` model; `UsageEnvelope` + `UsageReported`,
`TokensInput/Output`, `CostUSD`; `Build(...)` получает usage и ставит
`TokensUnknown = !usage.Attested`. Call-site один — `finalize.go`
(writeUsageEnvelope), конвертер `usageToMetrics` держит metrics без
зависимости на runtime.

## Risks

- claude/codex ParseUsage может давать partial data — это не faux: берём что
  аттестовано, остальное unknown + zero.
- Looser attempt-порядок: cap считается по суммарным попыткам ВСЕХ стадий
  (включая replay при resume). Для дефолта 100 это безопасно; для явных
  маленьких значений понадобится точная настройка.