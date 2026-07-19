## 1. pkg/config — Transition, MaxRetries, and SummaryConfig

- [ ] 1.1 Добавить поля `Transition string` и `MaxRetries int` в `AgentConfig` структуру
- [ ] 1.2 Добавить парсинг `transition` (default: "auto") и `max_retries` (default: 0) в `UnmarshalYAML`
- [ ] 1.3 Обновить `AgentConfig()` — возвращать заполненный объект с fallback значениями

## 2. pkg/ui — Persistent Status Bar

- [ ] 2.1 Переписать `ProgressBar` на ANSI escape: `\033[s` (save cursor), `\033[u` (restore), `\033[K` (clear line)
- [ ] 2.2 Создать `StatusBar` — нижняя строка: `[ai-team] <project> | <feature> | <agent> (N/M)`
- [ ] 2.3 Создать `StatusWriter` — обёртка `io.Writer`, перерисовывает status bar после каждого `\n`
- [ ] 2.4 Интегрировать StatusBar в PipelineStatus вместо ProgressBar
- [ ] 2.5 Отключать status bar если stdout не терминал

## 3. pkg/pipeline — Git Diff Guard

- [ ] 3.1 Создать функцию `hasGitChanges(dir string) bool` — `git diff --quiet` + `git status --porcelain`
- [ ] 3.2 Создать функцию `hasGitDir(dir string) bool` — проверка `git rev-parse --git-dir`
- [ ] 3.3 После `rt.Execute()` если у агента `len(outputs) == 0` — запускать `hasGitChanges`
- [ ] 3.4 Если `hasGitChanges == false` → `r.Err` с сообщением "агент X не создал изменений"

## 4. pkg/pipeline — Workflow Transitions (auto/by_confirm)

- [ ] 4.1 После завершения агента проверять `agent.Transition`
- [ ] 4.2 Если `by_confirm` — показать `Continue to <next>? [Y/n/diff/summary]`
- [ ] 4.3 Реализовать обработку ответов: Y→continue, n→stop, diff→git diff, summary→show summary
- [ ] 4.4 Проверка `isatty(stdin)` — если не терминал, `by_confirm` = `auto`

## 5. pkg/pipeline — Loopback on REJECTED

- [ ] 5.1 После reviewer читать verdict из output файла (REJECTED/CHANGES_REQUESTED/APPROVED)
- [ ] 5.2 Если REJECTED/CHANGES_REQUESTED и `max_retries > 0` и retries не исчерпаны — показать приглашение
- [ ] 5.3 Реализовать retry coder-а: добавить review.md как input, запустить coder → reviewer снова
- [ ] 5.4 Счётчик retries — остановка при превышении `max_retries`
- [ ] 5.5 При APPROVED — continue to tester как обычно

## 6. pkg/report — Stage Summaries

- [ ] 6.1 В `GenerateStageReport` искать `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md`
- [ ] 6.2 Читать первые 200 символов summary в структуру данных
- [ ] 6.3 Обновить `templates/stage.html` — секция Summary
- [ ] 6.4 Обновить `templates/final.html` — колонка Summary в таблице этапов

## 7. Verify and Test

- [ ] 7.1 `make test` — все тесты проходят
- [ ] 7.2 `make build` — бинарник собирается
- [ ] 7.3 Проверить `by_confirm` — пауза после этапа работает
- [ ] 7.4 Проверить `git diff guard` — coder без изменений останавливает пайплайн
- [ ] 7.5 Проверить loopback — REJECTED → coder retry
- [ ] 7.6 Проверить status bar — отображается и не затирается
- [ ] 7.7 Проверить stage summary в HTML-отчёте
