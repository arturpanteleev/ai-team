# Руководство разработчика

Вы здесь, если меняете **сам ai-team**: Go-контроллер, runtime-адаптер,
встроенного агента, dashboard или документацию. Для запуска ai-team над
своим продуктом начните с [учебника](tutorial/install.md) и [руководств](guides/troubleshooting.md).

## Поднять проект локально

Нужны Git, Go версии из [go.mod](../go.mod), Node.js 22+ и npm для frontend
и OpenSpec validation. Для unit/E2E проверок API-ключи не нужны: E2E используют
mock-opencode. Live runtime нужен только для отдельной интеграционной проверки.

```bash
git clone https://github.com/arturpanteleev/ai-team.git
cd ai-team
make build
make test
make docs
```

Собранный CLI: `bin/ai-team`. Документация: `docs/_site/index.html`.
Для локального просмотра с корректными абсолютными ссылками запустите
статический HTTP-сервер (если установлен Python 3):

```bash
python3 -m http.server 8000 --bind 127.0.0.1 --directory docs/_site
```

Откройте `http://127.0.0.1:8000/`. Поиск выполняется в браузере и не отправляет
запросы внешнему сервису. Выключение JavaScript сохраняет страницы и навигацию.

## Найти место изменения

| Что меняете | Начните здесь | Что проверить рядом |
|---|---|---|
| Переход или исход стадии | `pkg/workflow`, `pkg/pipeline` | Resume, loopback, invalidation и attempts |
| Новый runtime | `pkg/runtime` | CLI argv, env isolation, project surface, process lifecycle |
| Verification adapter | `pkg/checks`, `pkg/junit` | Required/optional, timeout, output bounds, workspace digest |
| Approval или delivery | `pkg/approval`, `pkg/delivery`, pipeline delivery files | Exact plan hash, stale candidate, retry/lock, negative cases |
| Агент / prompt | `agents/<name>/` | def.yaml contract, inputs/outputs, scope, verdict |
| HTTP / dashboard | `pkg/web`, `web/` | Auth/RBAC, projections, frontend build parity |
| Документация | `docs/`, `README.md`, `docsgen/` | Links, headings, search, base-path и runnable examples |

[Полная карта модулей](MODULES.md) перечисляет каждый пакет и его границу.
[Архитектура](ARCHITECTURE.md) объясняет инварианты и движение данных.

## Изменить поведение через контракт

Проект следует OpenSpec. Если меняется наблюдаемый продуктовый контракт,
сначала опишите изменение в `openspec/changes/`: proposal → design → specs →
tasks → implementation → archive. Checkpoints согласуются с reviewer;
исключение — его явное разрешение выполнить весь цикл сразу.

Документация, опечатки и тест уже принятого контракта не требуют formal
change. Полный нормативный процесс находится в [CONTRIBUTING](../CONTRIBUTING.md),
а инструментальные инструкции для Claude Code — в [CLAUDE.md](../CLAUDE.md).
Это checkpoints разработки проекта, а не runtime delivery approval.

## Проверить изменение

Во время работы запускайте затронутый пакет:

```bash
go test -count=1 ./pkg/pipeline/...
go test -count=1 ./docsgen/...
```

Перед передачей PR выполните:

```bash
make verify
make docs
```

`make verify` проверяет OpenSpec, формат Go, модули, vet, govulncheck, race,
coverage, E2E, runnable demo и frontend build/lint/tests/audit. Это может
занять несколько минут и требует сети для установки инструментов/зависимостей.
Для E2E после изменения runtime используйте `-count=1`, чтобы не принять
закэшированный subprocess-тест за новое исполнение.

Для изменения dashboard:

```bash
(cd web && npm ci && npm run lint && npm test && npm run build)
```

Обновлённый `web/dist` входит в PR вместе с `web/src`. `docs/_site` — generated
output: его не коммитят, Pages собирает сайт из Markdown и docsgen.

## Какие инварианты нельзя потерять

- LLM предлагает artifact/verdict; контроллер определяет переход и эффекты.
- Положительный verdict не отменяет провал required check.
- Mutation scope и reviewed candidate identity проверяются повторно.
- Checkpoint не разрешает commit/push/PR; delivery требует exact plan approval.
- Resume проверяет evidence/provenance; stale решение не становится свежим.
- Warning, skipped и unknown остаются видимыми, не превращаются в pass.
- Вывод для машин остаётся парсируемым; секреты не должны попадать в публикуемый bundle.

Для каждого изменения на границе доверия добавляйте отрицательный сценарий:
неверный SHA, changed candidate, broken evidence, отсутствующий runtime,
failed required check или запрещённый путь. Тест должен проверять отказ
опасного действия, а не только наличие текста ошибки.

## Подготовить PR

Опишите проблему, итоговое поведение и выполненные проверки. Для UI/docs
приложите desktop/mobile preview; для CLI — короткий обезличенный пример.
Сохраните существующие anchors или добавьте совместимость: на документацию
ссылаются README, issues и внешние пользователи.

Изменения, значимые для пользователя, добавьте в `CHANGELOG.md → Unreleased`.
Не описывайте ещё не выпущенное исправление как доступное через `@latest`.
Публикацию release и Pages выполняет GitHub workflow после соответствующего
Git-события; локальная сборка не меняет публичный сайт.
