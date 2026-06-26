## 1. GitHub Actions CI

- [x] 1.1 Создать `.github/workflows/ci.yaml` с go build, go test, go vet на push и PR
- [x] 1.2 Добавить CI-badge в README.md

## 2. Mock opencode

- [x] 2.1 Создать `testdata/mock-opencode.sh` с парсингом `--message-file` → создано в `e2etest/mock-opencode.sh`
- [x] 2.2 Реализовать создание output-файлов для каждого агента (Analyst, Architect, Coder, Reviewer, Tester)
- [x] 2.3 Реализовать Deployer: проверка review APPROVED и test-report PASS
- [x] 2.4 Добавить сценарий ошибки: agent.sh для REJECTED review и FAIL test-report

## 3. E2E-тесты

- [x] 3.1 Создать `testdata/e2e_test.go` — тест успешного пайплайна → создано в `e2etest/e2e_test.go`
- [x] 3.2 Добавить тест: пайплайн падает при REJECTED review
- [x] 3.3 Добавить тест: `ai-team init` создаёт структуру
- [x] 3.4 Проверить что тесты проходят: `go test ./...` — все 7 пакетов OK

## 4. Eval-пакет и CLI

- [x] 4.1 Создать `pkg/eval/eval.go` с типами Eval, Result
- [x] 4.2 Реализовать `eval.Run()` — чтение артефакта + вызов opencode-судьи
- [x] 4.3 Добавить команду `ai-team eval` в CLI
- [x] 4.4 Написать тесты для pkg/eval

## 5. Финальная проверка

- [x] 5.1 `go build ./cmd/ai-team` — успех
- [x] 5.2 `go test ./...` — все тесты проходят
- [x] 5.3 `go vet ./...` — чисто
