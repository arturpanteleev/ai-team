## 1. Создание агента verifier

- [x] 1.1 Создать `agents/verifier/def.yaml` с inputs (proposal, specs, review, test-report) и outputs (verification)
- [x] 1.2 Создать `agents/verifier/prompt.md` с инструкциями verification pass: проверка AC, self-review, DoD checklist
- [x] 1.3 Проверить что def.yaml валиден и agents/verifier/ содержит оба файла

## 2. Интеграция в registry

- [x] 2.1 Обновить `Registry.DefaultPipeline()` в `pkg/agent/registry.go` — добавить verifier между tester и deployer
- [x] 2.2 Обновить `config.Default()` в `pkg/config/load.go` — новый дефолтный pipeline

## 3. Тесты

- [x] 3.1 Запустить `make build` для проверки что агент verifier загружается
- [x] 3.2 Запустить `make test` для проверки что изменения не сломали существующие тесты
