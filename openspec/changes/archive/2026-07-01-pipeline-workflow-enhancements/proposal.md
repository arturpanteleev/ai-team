## Why

После прогона на arcanoid (feature «Новый фон, для птички») выявились критические проблемы в пайплайне:

1. **Coder ничего не сделал**, но пайплайн посчитал это успехом — `outputs: {}` в `def.yaml` привело к нулевой проверке.
2. **Нет контроля над переходом между этапами** — пайплайн летит вперёд даже после сомнительного результата.
3. **Progress bar — фейковый** — `\r` без сохранения позиции, затирается логами.
4. **REJECTED = полный стоп** — reviewer отклонил, но вернуться к coder-у нельзя без ручного `--retry-from`.
5. **Отчёты бедные** — только списки файлов, нет текстовой сводки от агента.

Нужно: git-diff проверка, настраиваемые переходы (auto/by_confirm), настоящий status bar, loopback при rejection, stage summaries в отчётах.

## What Changes

1. **Git diff guard** — после каждого агента (или только с пустыми outputs) проверять `git diff --quiet && git status --porcelain` в targetDir. Если нет изменений — пайплайн останавливается с ошибкой.
2. **Workflow transitions** — поле `transition: auto|by_confirm` для каждого агента в `config.yaml`. При `by_confirm` после этапа: `Continue to <next>? [Y/n/diff]`.
3. **Persistent status bar** — нижняя строка терминала, сохраняемая между логами (ANSI escape). Показывает проект, фичу, текущего агента, прогресс.
4. **Loopback on REJECTED** — при вердикте REJECTED (или CHANGES_REQUESTED) pipeline предлагает отправить обратно coder-у с review.md как входом, счётчиком retries и лимитом из конфига.
5. **Stage summaries** — каждый агент оставляет `.stage-summary/{agent}.md` со свободным текстом о том, что сделано/найдено. Отчёты включают эту сводку.

## Capabilities

### New Capabilities
- `git-diff-guard`: Проверка изменений в target-проекте через git diff/status после каждого агента
- `workflow-transitions`: Настраиваемые правила перехода между этапами (auto/by_confirm)
- `persistent-status-bar`: Настоящий status bar внизу терминала, не затираемый логами
- `workflow-loopback`: Автоматический возврат к предыдущему агенту при REJECTED/CHANGES_REQUESTED
- `stage-summary`: Текстовые сводки от каждого агента в HTML-отчётах

### Modified Capabilities
- `role-config`: Добавить поле `transition` в `AgentConfig`
- `agent-orchestration`: Логика loopback, git-diff guard, by_confirm — часть оркестрации
- `html-reports`: Stage summaries в HTML-шаблоны

## Impact

- `pkg/config/config.go` — новое поле `Transition string` в AgentConfig
- `pkg/pipeline/pipeline.go` — git diff check, by_confirm пауза, loopback цикл, status bar
- `pkg/ui/progress.go` — переписать на persistent status bar с ANSI escape
- `pkg/agent/agent.go` — опционально добавить `StageSummaryFile` или договориться о конвенции
- `pkg/report/templates/` — обновить stage.html, final.html для summary
- `pkg/report/generator.go` — читать `.stage-summary/*.md` и передавать в шаблон
- `agents/coder/def.yaml` — (не менять) git-diff guard покрывает проблему без changes
