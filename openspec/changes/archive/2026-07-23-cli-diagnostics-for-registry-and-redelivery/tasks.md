## 1. OpenSpec-артефакты

- [x] 1.1 proposal.md
- [x] 1.2 design.md
- [x] 1.3 specs/cli-interface/spec.md (MODIFIED)
- [x] 1.4 tasks.md (этот файл)

## 2. Finding 9 — `ai-team list` не должен терять агентов молча

- [x] 2.1 `agent.Registry.List()` → `(agents []*Agent, failures []LoadFailure)`
- [x] 2.2 `cmd/ai-team/main.go cmdList()` печатает failures в stderr
- [x] 2.3 Обновить существующий вызов в `pkg/agent/registry_test.go`
- [x] 2.4 Новый тест: невалидный, не-shadowing agent виден в failures, не
      теряется среди валидных

## 3. Finding 10 — повторный run уже доставленной фичи

- [x] 3.1 `evidence.FindDelivered(runsRoot, feature)` — сканирует run.json +
      attempt manifests, ищет deployer attempt с непустым CommitSHA/PRURL
- [x] 3.2 `cmd/ai-team/main.go warnIfAlreadyDelivered` — вызывается из
      `cmdRun()` только для свежего run (не `--retry-from`)
- [x] 3.3 Тесты: находит доставленный run; игнорирует другие фичи и
      незавершённые/partial delivery attempts; not-found на отсутствующем
      runs root

## 4. Verification

- [x] 4.1 Мутационное тестирование обеих новых проверок (временная правка +
      `go test`, затем revert)
- [x] 4.2 `make specs`
- [x] 4.3 `go test -count=1 ./...`
- [x] 4.4 `gofmt -l .` / `go vet ./...`
