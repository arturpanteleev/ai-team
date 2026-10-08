# ai-team

[![CI](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml/badge.svg)](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml)
[![Docs](https://img.shields.io/badge/docs-site-blue)](https://arturpanteleev.github.io/ai-team/)
[![Release](https://img.shields.io/github/v/release/arturpanteleev/ai-team)](https://github.com/arturpanteleev/ai-team/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/arturpanteleev/ai-team)](https://go.dev/dl/)
[![License](https://img.shields.io/github/license/arturpanteleev/ai-team)](LICENSE)

**AI-агенты пишут код. Решает, что попадёт в репозиторий, не модель, а вы и
строгий контроллер.**

ai-team ведёт задачу по цепочке агентов: аналитик пишет спецификацию,
архитектор — дизайн, программист — код, ревьюер, тестировщик и верификатор
проверяют. Переходы между этапами, проверки, доказательства и delivery
исполняет детерминированный контроллер на Go. Коммит, push и PR появляются
только после того, как вы подтвердили точный delivery-план по его SHA-256.

Документация: **<https://arturpanteleev.github.io/ai-team/>**

## Зачем это

С обычным coding-агентом трудно быть уверенным, что между «агент написал код»
и «код попал в main» были ревью и тесты, а не «похоже, ок». ai-team превращает
это в обязательный протокол:

- каждый смысловой этап заканчивается вердиктом, который читает контроллер,
  а не человек на глаз;
- агент может менять только разрешённые пути, всё остальное проваливает этап;
- проверки проекта запускает контроллер, а не модель;
- delivery делает контроллер, ровно по плану, который вы подтвердили;
- каждый прогон оставляет проверяемые доказательства в `.ai-team/runs/<run_id>/`.

Как это выглядит на экране — в [туре](docs/start/tour.md). Чем ai-team
отличается от Bernstein, AI-SDLC, Spec Kit, LangGraph и других — в
[сравнении](docs/start/compare.md).

## Для кого

**Для команды.** ai-team разворачивается на сервере или в облаке, и команда
работает в одном веб-дашборде: запускает прогоны, подтверждает переходы по
своим ролям, смотрит артефакты и доказательства. Попробовать можно и на своей
машине, на одном репозитории.

**Enterprise — следующий этап.** Для одной команды уже есть вход по токену,
роли в подтверждениях, очередь заданий и архив доказательств. Разделения
ответственности между командами и зонами, управления пользователями и
корпоративного SSO пока нет.

> [!NOTE]
> ai-team не изолирует агентов сам: агент работает с правами процесса, который
> его запустил. В облаке запускайте исполнителей в одноразовых контейнерах.
> Подробно — в [Граница безопасности](docs/reference/security.md).

Подробнее о выборе — [Подходит ли вам](docs/start/fit.md).

## Установка

Нужны Go 1.26.9+ (для `go install`), один из CLI-рантаймов в `PATH`
([OpenCode](https://opencode.ai), [Codex](https://github.com/openai/codex) или
[Claude Code](https://docs.anthropic.com/en/docs/claude-code)) и, для delivery,
авторизованный [`gh`](https://cli.github.com).

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
ai-team version
```

Готовые бинарники для darwin/linux × amd64/arm64 лежат в
[релизах](https://github.com/arturpanteleev/ai-team/releases/latest). Каждый
релиз после `v0.2.0` подписан cosign (keyless, Sigstore) и снабжён
`sha256sums.txt`. Как проверить подпись перед установкой — в учебнике
[Установка](docs/tutorial/install.md).

## Быстрый старт

Посмотреть `ai-team gate` без модели и без ключей — готовое демо из корня
репозитория ai-team (нужны `bash`, `git` и Go):

```bash
bash docs/demo/run-demo.sh
```

Подготовить свой проект и провести задачу:

```bash
cd /my-project
ai-team init
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию"
```

В интерактивном терминале контроллер покажет delivery-план и спросит
`Продолжить? [y/N]` перед delivery. Без терминала (CI, скрипт)
прогон остановится с кодом `3` и напечатает delivery-план с его SHA-256.
Подтвердите именно его:

```bash
ai-team run --resume <run_id> --approve-plan <sha256>
```

Результат — на дашборде `ai-team web` или в `.ai-team/runs/<run_id>/`.

Ключ провайдера агент видит, только если вы разрешили его имя:
`AI_TEAM_HARNESS_ENV_ALLOW=ANTHROPIC_API_KEY ai-team run …`. Подробно — в
учебнике [Подключить настоящую модель](docs/tutorial/real-model.md).

## Документация

| Раздел | Что внутри |
|---|---|
| [Ключевые понятия](docs/start/concepts.md) | прогон, кандидат, вердикт, delivery-план; глоссарий |
| [Учебник](docs/tutorial/install.md) | от установки до первого PR за четыре шага |
| [Проверки в CI без модели](docs/guides/ci-gate.md) | `ai-team gate` как проверка изменений в CI |
| [Остановки и BLOCKED](docs/guides/troubleshooting.md) | почему прогон остановился и что делать |
| [Команды CLI](docs/reference/cli.md) | все команды и флаги |
| [Конфигурация](docs/reference/config.md) | `.ai-team/config.yaml`, профили, рантаймы и ключи |
| [Агенты и этапы](docs/reference/pipeline.md) | кто что делает и где решает человек |
| [Статусы и коды выхода](docs/reference/statuses.md) | что означает код и что делать |
| [Граница безопасности](docs/reference/security.md) | что гарантируется, а что нет |
| [Архитектура](docs/ARCHITECTURE.md) | как устроен контроллер внутри |

## Разработка

Нужны Go 1.26.9+ и Node 22+ (фронтенд дашборда); точная версия Go для
локальной сборки закреплена в [`.tool-versions`](.tool-versions).

```bash
make build          # сборка bin/ai-team
make test           # go test ./...
make test-e2e       # E2E на mock-opencode
make specs          # строгая проверка OpenSpec
make docs           # сайт документации в docs/_site
make verify         # полная проверка, как в CI
```

Как устроен процесс, как предложить изменение и что проверяет CI — в
[CONTRIBUTING.md](CONTRIBUTING.md) и [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
Ошибки и предложения — в
[GitHub Issues](https://github.com/arturpanteleev/ai-team/issues).
Уязвимости — только приватно, по [SECURITY.md](SECURITY.md).

Сайт документации собирается генератором [`docsgen/`](docsgen/) из Markdown
этого репозитория и публикуется на GitHub Pages при push в `master`. Релизы
собираются по тегу `v*`: сначала весь CI против коммита тега, потом сборка,
подпись cosign и публикация.

## Лицензия

Apache License 2.0 — см. [LICENSE](LICENSE).
