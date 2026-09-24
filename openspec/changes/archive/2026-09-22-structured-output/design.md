# Structured output (OPS-6) — design

## Маршрутизация: один флаг-режим на процесс

`cmd/ai-team` — одна точка входа; режим вывода глобален на процесс (никакого
пери-команда состояния в пакетах). `applyOutputMode()` в `main` сканирует
`os.Args[1:]` на `--json`/`--quiet`/`-q` ДО диспатча, вызывает
`logging.SetMode`, и ВЫРЕЗАЕТ эти флаги из `os.Args`. Почему вырезание, а не
регистрация в каждом FlagSet: 14 подкоманд с `flag.ExitOnError` — добавление
флага в каждый флагсет = 14 правок и риск рассинхрона; вырезание работает для
всех сразу без изменения подкоманд. Флаги распознаются и до и после имени
подкоманды, поскольку сканируется весь хвост начиная с `os.Args[1]`.

## `pkg/logging`: Emitter + Record

- `Mode` (`Default`/`Quiet`/`JSON`), глобальный `emitter`.
- `Emit(Record)`: 
  - **JSON-mode**: `json.Marshal(record)` → stdout (одна строка на запись).
  - **human**: префикс `✓/✗/⚠/•` → ok на stdout, остальное на stderr;
    в `Quiet` — ok подавляется, критичное (error/warning) остаётся.
- `Record` — стабильная схема: `level`, `cmd`, `type`, `message`, `data`
  (map[string]any), `exit_code`. Совместимость: новые поля — `omitempty`.

## Точки маршрутизации (минимальный, стабильный контракт)

Машиночитаемому потребителю нужны только **результаты** команд, не каждый
шаг. Поэтому эмитим record в 4 местах:

1. `cmdRun` (после успешного завершения): `{cmd:run, type:run, data:{run_id,
   feature, outcome}, exit:0}`.
2. `cmdVerify`: `{cmd:verify, type:gate_bundle|run_bundle|run, data:{...},
   exit:0}` на success; на fail — `{level:error, ...}` перед `os.Exit`.
3. `cmdExport`: `{cmd:export, type:bundle, data:{run_id, bundle,
   bundle_sha256, signed}, exit:0}`.
4. `cmdGate`: `{cmd:gate, type:gate, data:{status, policy, bundle_sha256,
   bundle, signed}, exit:gate.ExitCode(result)}`.

Human-вывод (цветные строки) сохраняется как есть — регрессии нет; JSON — это
доп. канал на stdout в JSON-режиме, а в quiet human-сводка уходит на stderr (не
пачкает stdout).

## Отказ от альтернатив

- **slog/std log** — поточно-логирование, а не стабильные результаты команд;
  для CI-контракта нужен явный `Record` с фиксированной схемой.
- **Переписывание всех человеческих строк** — риск регрессии UI; оставляем
  human как есть, JSON только для результатов.
- **env-переменная вместо флага** — флаг явнее и документирован в usage.

## Риски

- Флаги `--json` конфликтуют с пользовательскими значениями? Нет: вырезание
  только точных токенов `--json`/`--quiet`/`-q`.
- `os.Args` мутируется — безопасно: `applyOutputMode` вызывается первой и
  сохраняет остальные аргументы и порядок.
- JSON `data` содержит только стабильные скаляры (строки/bool) — без структур
  с зависимым layout, чтобы схема не ломалась между версиями.
