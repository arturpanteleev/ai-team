# Tasks — Structured output (OPS-6)

## T1. pkg/logging

- [x] `pkg/logging/logging.go`: `Mode` (`Default`/`Quiet`/`JSON`), глобальный
      `emitter`, `SetMode`, `GetMode`, `Record{Level, Command, Type, Message,
      Data, Exit}`, `Emit(Record)`:
      JSON-mode → `json.Marshal` строкой на stdout; human → ок на stdout с
      `✓`, остальное на stderr; quiet → подавить ok на stdout, критичное
      оставить.
- [x] `pkg/logging/logging_test.go`: JSON record парсится в `Record` со всеми
      полями; error-ветка не пишет в err при ok; `SetMode`/`GetMode` roundtrip.

## T2. CLI global flags

- [x] `applyOutputMode()` в `main`: сканирует `os.Args[1:]` на
      `--json`/`--quiet`/`-q`, вызывает `logging.SetMode`, ВЫРЕЗАЕТ эти токены
      из `os.Args` (до/после подкоманды, порядок остальных аргументов сохранён).
- [x] usage text: «Глобальные флаги вывода (OPS-6): `--json`, `--quiet/-q`».

## T3. Маршрутизация результатов команд

- [x] `cmdRun`: после успеха эмитит `{cmd:run,type:run,data:{run_id,feature,
      outcome},exit:0}` в JSON/quiet.
- [x] `cmdVerify`: success `{cmd:verify,type:gate_bundle|run_bundle|run,...}`,
      fail `{level:error,...}` перед `os.Exit`.
- [x] `cmdExport`: `{cmd:export,type:bundle,data:{run_id,bundle,bundle_sha256,
      signed},exit:0}`.
- [x] `cmdGate`: `{cmd:gate,type:gate,data:{status,policy,bundle_sha256,bundle,
      signed},exit:gate.ExitCode(result)}`.

## T4. Verification

- [x] `go build ./...`, `go vet`, `gofmt -l` — чисто.
- [x] CLI smoke: `gate --json` → одна JSON-строка; `verify --json` → JSON;
      `--quiet` → stdout без JSON, exit тот же.
- [x] `make specs` 65/65; `go test ./...` зелёные (кроме двух e2e baseline).
