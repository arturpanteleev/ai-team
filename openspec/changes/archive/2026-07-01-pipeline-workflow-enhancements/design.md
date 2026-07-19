## Context

Текущий пайплайн (`pkg/pipeline/pipeline.go`) — линейный проход по списку агентов с проверкой входов/выходов артефактов. После прогона на arcanoid выявились проблемы:

1. **Coder: `outputs: {}`** — выходные артефакты не заданы, проверка пропускается. Coder мог не создать ни одного файла, а pipeline считает это успехом.
2. **Нет пауз** — pipeline бежит без остановки, нельзя глянуть промежуточный результат и решить.
3. **Progress bar не сохраняется** — `\r` затирается следующим логом.
4. **REJECTED = стоп** — reviewer отклонил, цикл обратной связи с coder-ом отсутствует.
5. **Отчёты без контекста** — только списки файлов без пояснений от агента.

## Goals / Non-Goals

**Goals:**
- Git diff guard: проверять, что coder действительно изменил файлы в проекте
- Workflow transitions: `auto` / `by_confirm` для каждого этапа
- Persistent status bar: нижняя строка, не затираемая логами
- Loopback: при REJECTED отправлять обратно coder-у с review.md, с лимитом retries
- Stage summaries: свободный текст от каждого агента в HTML-отчёте

**Non-Goals:**
- Полный графовый workflow (DAG) — только линейный с loopback
- GUI для confirm — только терминал (stdin)
- Отправка email/telegram при rejection — только консоль
- Изменение runtime или registry

## Decisions

### D1. Git diff guard — только для coder-а (и агентов с пустыми outputs)

Проверка запускается после `rt.Execute()` если у агента `len(outputs) == 0`:

```go
func hasGitChanges(dir string) bool {
    // tracked files changed
    if exec.Command("git", "diff", "--quiet").Run() != nil {
        return true
    }
    // new untracked files
    out, _ := exec.Command("git", "status", "--porcelain").Output()
    return len(bytes.TrimSpace(out)) > 0
}
```

- Если `hasGitChanges` == false → `r.Err = "агент X не создал изменений"`
- **Почему не для всех агентов?** Только coder пишет в исходники. Остальные — в `.ai-team/artifacts/`.
- **Что если проект не git?** Проверять `git rev-parse --git-dir` перед diff. Если не git — skip с warning.

### D2. Transitions — auto / by_confirm

Поле `Transition` в `AgentConfig`. Значения:
- `auto` (default) — pipeline идёт дальше без остановки
- `by_confirm` — после этапа:

```
✓ coder completed (3.2s)
─────────────────────────────────────────
Continue to reviewer? [Y/n/diff/summary]
  Y         → yes, continue
  n         → stop pipeline
  diff      → show git diff, then prompt again
  summary   → show stage summary, then prompt again
```

Реализация: `fmt.Scanln()` после этапа, если `transition == by_confirm`. Если ответ `n` → `return nil` (пайплайн завершён досрочно).

### D3. Persistent status bar — ANSI escape sequences

Техника: после каждого вывода (лог, notifier, и т.д.) перерисовывать нижнюю строку.

Схема работы:
```
\033[s              — save cursor position
<agent log output>  — обычный print
\033[u              — restore cursor to saved position
\033[K              — clear line (eraser from cursor to EOL)
<status bar>        — draw the bar
```

Проблема: логи (особенно от opencode) пишут много строк. Нужно:
1. После каждого `fmt.Print`/`fmt.Println` перерисовывать статус-бар
2. Либо использовать обёртку `StatusWriter` которая пишет в stdout и после каждого `\n` перерисовывает бар

Выбран подход **#2** — обёртка `pkg/ui/status_writer.go`:

```go
type StatusWriter struct {
    mu     sync.Mutex
    bar    string
}
func (w *StatusWriter) Write(p []byte) (n int, err error) {
    // 1. output the bytes
    // 2. redraw status bar
}
```

Pipeline передаёт `StatusWriter` вместо `os.Stdout` через `os.Pipe` или замену `os.Stdout` — нет, это опасно. Лучше: pipeline принимает `io.Writer` и все `fmt.Fprintf(w, ...)`.

**Упрощение**: перерисовывать статус-бар после каждого этапа (между агентами), а не после каждой строки лога. Это проще и достаточно для UX. Во время выполнения агента (opencode) bar не обновляется — opencode управляет своим выводом сам.

### D4. Loopback — reviewer → coder

Pipeline после reviewer читает `verdict` из output файла:
- `APPROVED` → continue to tester
- `CHANGES_REQUESTED` → prompt: "Send back to coder? [Y/n]", если Y → loop
- `REJECTED` → prompt: "Send back to coder? [Y/n]", если Y → loop

Loopback механизм:
1. Сохранить `results` до coder-а
2. Добавить `review.md` как дополнительный input для coder-а
3. Запустить coder снова
4. Максимум `maxRetries` раз (из конфига)
5. Если все retries исчерпаны → stop с ошибкой

```yaml
pipeline:
  - name: coder
    max_retries: 2
  - name: reviewer
```

На каждом retry reviewer проверяет снова. Если retries кончились — финальный вердикт.

### D5. Stage summaries — конвенция .stage-summary/

Каждый агент может оставить файл `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md` со свободным текстом.

- **Кто пишет?** Prompt агента просит: "After your work, write a summary to `.stage-summary/{agent}.md`"
- **Кто читает?** `report.GenerateStageReport()` ищет этот файл после агента
- **Формат:** Markdown-файл, первые 200 символов показываются в HTML-отчёте как summary

Альтернатива: агент пишет summary прямо в начале своего output-файла. Но output-файлы имеют свою структуру (review.md — вердикт, design.md — дизайн). Файл summary — отдельный, гибкий.

### D6. Обновление конфига

```yaml
pipeline:
  - name: analyst
    transition: auto
  - name: coder
    transition: by_confirm
    max_retries: 2
  - name: reviewer
    transition: auto
    max_retries: 2
  - name: tester
  - name: deployer
```

Поля `transition` и `max_retries` опциональны, fallback на `auto` и `0` (без ретраев).

## Risks / Trade-offs

- **[Status bar + opencode вывод]** opencode пишет напрямую в stdout, bypass-ит обёртку. **Митигация**: bar перерисовывается только между агентами, не во время выполнения opencode.
- **[Git diff race]** Если агент модифицирует файл, но команда `git` не успевает — ложное срабатывание. **Митигация**: diff проверяется только после `rt.Execute()`, который синхронный — race condition исключён.
- **[Loopback бесконечный]** coder и reviewer могут зациклиться. **Митигация**: `max_retries` в конфиге, жёсткий лимит.
- **[by_confirm в неинтерактивном режиме]** `--target` может быть на CI, stdin недоступен. **Митигация**: если stdin не терминал (`!isatty`), `by_confirm` работает как `auto`.
