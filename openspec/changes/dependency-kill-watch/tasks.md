# Dependency kill-watch tracker (EXP-1) — tasks

1. `docs/DEPENDENCY-KILL-WATCH.md`: таблица (сценарий / сигнал / действие) на
   4 сценария, секция «Где наблюдать» с источниками, порог pivot (≥3 интервью
   «не нужно» или 0 повторных запусков за 8 недель).
2. `docs/docs_test.go`: `TestDependencyKillWatchTracksSignals` — документ
   содержит сценарии, сигналы, действие и Stop-правило; ссылка на
   FEATURES.cloud.md корректна.
3. OpenSpec delta `dependency-kill-watch`.

Verification: `go test ./docs/`, `go build ./...`, `gofmt -l`, `make specs`
(73/73).