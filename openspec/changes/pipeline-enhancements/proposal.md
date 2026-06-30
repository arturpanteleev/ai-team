## Why

После успешного прохода пайплайна по arcanoid (PR #6) стало очевидно, что текущий консольный вывод недостаточен для реальной разработки. Пользователь хочет: видеть HTML-отчёты с ссылками на артефакты, получать уведомления (сейчас в консоль, в будущем — email/telegram), цветной прогресс-бар, гибкую настройку модели/CLI для каждой роли и возможность отката при ошибке.

## What Changes

1. **HTML-отчёты** — каждый агент после завершения генерирует `.ai-team/reports/{feature}/{agent}/report.html` с описанием что сделано, ссылками на артефакты, статусом. В конце — итоговый `report.html` со сводкой по всем этапам.
2. **Notifier interface** — `pkg/notifier/` с интерфейсом `Notifier { Notify(ctx, stage Result) }`. Первая реализация — `ConsoleNotifier`. Расширяемый дизайн для email/telegram в будущем.
3. **Progress bar / status line** — нижняя строка в терминале: `[ai-team] project: arcanoid | feature: fix-game-start | agent: coder ████████░░ 4/6`.
4. **Цветной вывод** — ANSI-цвета для консоли: зелёный для ✓, красный для ✗, жёлтый для названий этапов, голубой для путей.
5. **Ролевая конфигурация** — `config.yaml` расширяется: каждый агент может иметь `model`, `effort`, `cli` (переопределение глобального). Пример:
   ```yaml
   pipeline:
     - name: analyst
       model: claude-sonnet-4-20250514
       effort: high
     - name: coder
       model: claude-opus-4-20250514
   ```
6. **Workflow rollback** — при ошибке на этапе N, пользователь может запросить повторный запуск начиная с этапа N-1 или N, а не с начала. `ai-team run --retry-from <agent>` или интерактивный режим.

## Capabilities

### New Capabilities
- `html-reports`: Генерация HTML-отчётов по каждому агенту и итогового сводного отчёта с ссылками на md-артефакты
- `notifier-system`: Расширяемая система уведомлений с интерфейсом и console-реализацией
- `progress-bar`: Строка состояния с проектом, фичей, текущим агентом и прогрессом
- `colored-output`: ANSI-цветной вывод консоли
- `role-config`: Переопределение model/effort/cli на уровне каждого агента в config.yaml
- `workflow-rollback`: Повторный запуск пайплайна с указанного этапа при ошибке

### Modified Capabilities
- `cli-interface`: Добавить флаг `--retry-from` для run, обновить вывод справки
- `agent-orchestration`: Верификация сохраняется, добавляется поддержка неполного запуска (retry-from), генерация отчётов и нотификации — часть оркестрации
- `project-init`: Обновить структуру `.ai-team/` — добавить `reports/` директорию

## Impact

- `pkg/pipeline/pipeline.go` — расширить StageResult, добавить генерацию отчётов, вызов notifier, отрисовку прогресс-бара
- `pkg/runtime/` — без изменений (отчёты генерирует pipeline, не runtime)
- `pkg/notifier/` — новый пакет (interface + console implementation)
- `pkg/config/` — распарсить ролевую конфигурацию pipeline
- `cmd/ai-team/main.go` — флаг `--retry-from`, цветной вывод, прогресс-бар
- `pkg/report/` — новый пакет для генерации HTML-отчётов
- `openspec/specs/` — 6 новых spec-файлов + 3 delta-spec
