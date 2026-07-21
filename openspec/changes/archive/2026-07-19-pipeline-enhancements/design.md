## Context

Текущий пайплайн (`pkg/pipeline/pipeline.go`) выводит сырой лог в консоль и таблицу summary в конце. Нет:
- HTML-отчётов с ссылками на артефакты
- Уведомлений о завершении этапов
- Прогресс-бара
- Цветов
- Конфигурации модели/CLI для каждой роли
- Возможности перезапуска с указанного этапа

Все 6 фич — надстройки над существующим пайплайном, не затрагивающие Runtime и Registry.

## Goals / Non-Goals

**Goals:**
- HTML-отчёты после каждого агента и итоговый сводный
- Notifier interface с console-реализацией (расширяемый)
- Прогресс-бар в нижней строке терминала
- Цветной ANSI-вывод
- Ролевая конфигурация в `config.yaml` (model, effort, cli per agent)
- Флаг `--retry-from <agent>` для перезапуска

**Non-Goals:**
- Реальная отправка email/telegram (только интерфейс)
- Веб-сервер для отчётов (просто генерация .html файлов)
- Интерактивный режим rollback (просто флаг `--retry-from`)
- Изменение Runtime/Registry/AgentCLI

## Decisions

### D1. HTML-отчёты: pkg/report/ с HTML-шаблонами
Отчёты — статические HTML-файлы в `.ai-team/reports/{feature}/`. Каждый агент генерирует `{agent}/index.html`. В конце — `index.html` со сводкой.
- **Почему не Markdown → HTML конвертер?** У нас уже есть md-артефакты. Отчёт — не конвертация, а структурированная сводка с метаданными (статус, время, ссылки на md).
- **embed шаблонов** — HTML-шаблоны в `pkg/report/templates/`, встроены в бинарник через `//go:embed`.

### D2. Notifier: pkg/notifier/ — interface + chain
```go
type Notifier interface {
    Notify(ctx context.Context, stage StageResult, task *Task) error
}
```
Console-реализация — `ConsoleNotifier`. В будущем — `EmailNotifier`, `TelegramNotifier`. Цепочка: `NotifierChain{console, email, telegram}`.

### D3. Прогресс-бар: pkg/ui/progress.go
Простейший прогресс-бар через `\r` (carriage return). Не библиотека (в Go нет стандартной), а ~30 строк:
```
[ai-team] arcanoid | fix-game-start | coder (4/6) ████████░░
```
Отключается, если stdout не терминал.

### D4. Цвета: pkg/ui/color.go
Константы ANSI:
```
ColorGreen, ColorRed, ColorYellow, ColorCyan, ColorReset
```
Только если stdout — терминал. Иначе — noop.

### D5. Ролевая конфигурация: расширение config.yaml
Текущий `config.yaml`:
```yaml
pipeline: [analyst, architect, coder, reviewer, tester, deployer]
cli: opencode
model: auto
```

Новый формат:
```yaml
pipeline:
  - name: analyst
    model: claude-sonnet-4-20250514
    effort: high
  - name: coder
    model: claude-opus-4-20250514
    effort: high
    cli: opencode
  - name: reviewer
    model: claude-sonnet-4-20250514
cli: opencode
model: auto
effort: medium
```

Обратная совместимость: старый формат `pipeline: [...]` → парсится как список имён с глобальными model/cli/effort.

### D6. Workflow rollback: --retry-from
Новый флаг `ai-team run --retry-from <agent>`. Pipeline пропускает всех агентов до указанного и запускает с него. Артефакты предыдущих этапов уже существуют — проверка входов всё равно сработает.

### D7. Отчёты генерирует Pipeline, не Runtime
StageResult после этапа передаётся в:
1. `Notifier.Notify()` — уведомление
2. `report.GenerateStageReport()` — HTML-файл
После цикла — `report.GenerateFinalReport()`.

## Risks / Trade-offs

- **[Прогресс-бар + логи]** Логи `fmt.Printf` и `\r`-прогресс конфликтуют в терминале. **Митигация**: прогресс-бар перед каждым этапом, логи во время этапа, после этапа — снова прогресс-бар с обновлённым статусом.
- **[HTML-шаблоны раздуют бинарник]** Шаблоны — десятки KB, не критично. **Митигация**: embed + минификация не требуется.
- **[config.yaml совместимость]** Старый формат `pipeline: [list]` используется тестами. **Митигация**: `Config.PipelineAgents` — слайс структур с fallback на глобальные поля.
- **[--retry-from и артефакты]** Если предыдущий этап завершился частично, перезапуск с N может получить некорректные артефакты. **Митигация**: pipeline проверяет входы Stat — если не хватает, ошибка.
