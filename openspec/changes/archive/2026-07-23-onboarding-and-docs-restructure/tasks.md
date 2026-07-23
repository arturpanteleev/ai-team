## 1. OpenSpec-артефакты

- [x] 1.1 proposal.md
- [x] 1.2 design.md
- [x] 1.3 specs/project-documentation/spec.md
- [x] 1.4 tasks.md (этот файл)

## 2. Документация

- [x] 2.1 Переписать `README.md` по новой структуре (для кого / установка /
      быстрый старт / путь поставки фичи / CLI-справочник / конвейер /
      конфигурация / evals / глоссарий / граница безопасности / разработка)
- [x] 2.2 Создать `docs/ARCHITECTURE.md` с перенесённым глубоким описанием
      (детерминированный контроль, evidence, deployer/canonical delivery
      plan, layered agent registry) и картой пакетов
- [x] 2.3 Создать `CONTRIBUTING.md` с человеко-читаемым OpenSpec-циклом,
      gate rule, make-таргетами и примером добавления built-in агента
- [x] 2.4 Добавить глоссарий обязательных терминов в README

## 3. Regression guard

- [x] 3.1 `docs/docs_test.go` — проверка обязательных секций/терминов/ссылок
- [x] 3.2 Подтвердить мутационным тестированием, что тест ловит удаление
      термина (временная правка + `go test`, затем revert)

## 4. Verification

- [x] 4.1 `make specs`
- [x] 4.2 `go test -count=1 ./...`
- [x] 4.3 `gofmt -l .`
- [x] 4.4 Вручную проверить все относительные ссылки между
      README/CONTRIBUTING.md/docs/ARCHITECTURE.md/AUDIT.md
