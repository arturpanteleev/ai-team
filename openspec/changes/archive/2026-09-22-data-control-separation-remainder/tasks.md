# Data/control separation remainder (OPS-8) — tasks

1. `pkg/verdict`: `StripDataRegions(src string) string` (CommonMark fenced
   blocks ` ``` `/`~~~` с info-строкой и незакрытым fence «до конца»; blockquote
   строки `>`; замена пустыми строками с сохранением позиций).
2. `pkg/verdict`: применение strip в `Parse`, `FromOutputsContract`,
   `ReadBlocked`/`ReadBlockedSince`. Тесты: fenced маркер → None/ошибка
   контракта; quoted BLOCKED → не блок; маркер вне регионов парсится как раньше;
   несколько маркеров считаются только из control-регионов.
3. `pkg/runtime`: тесты адаптеров на чужие event-типы (`codex_test.go`,
   `claude_test.go`): model-mimic JSON игнорируется, последний реальный
   event остаётся источником.
4. OpenSpec delta `data-control-separation-remainder`.

Verification: `go build ./...`, `go vet`, `gofmt -l`, `go test ./...` (только 2
известных e2e baseline fail), `make specs` (71/71).