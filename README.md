# ai-team

[![CI](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml/badge.svg)](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml)

CLI-инструмент для запуска пайплайна AI-агентов в любом проекте.

## Установка

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
```

## Использование

```bash
# Инициализация в проекте
cd /my-project
ai-team init

# Запуск пайплайна
ai-team run --feature "add-jwt-auth" --task "Реализовать JWT авторизацию с refresh токенами"

# Список агентов
ai-team list
```

## Агенты

| Агент | Роль | Вход | Выход |
|---|---|---|---|
| analyst | System Analyst | task.md | proposal.md + specs/ |
| architect | Архитектор | specs/ | design.md + tasks.md |
| coder | Coder | design.md + tasks.md | код в проекте |
| reviewer | Reviewer | specs/ | review.md |
| tester | Tester | specs/ + review.md | test-report.md |
| deployer | Deployer | review.md + test-report.md | git commit + PR |

## Архитектура

Агенты общаются через артефакты в формате OpenSpec:
`.ai-team/artifacts/{feature}/{proposal|specs|design|tasks|review|test-report}`.

Каждый агент — вызов `opencode --resume --message-file` с контекстом из артефактов.

## Разработка

Проект использует [OpenSpec](https://openspec.pro) для spec-driven разработки.

```bash
/opsx:propose "новая-фича"
/opsx:apply
/opsx:archive
```
