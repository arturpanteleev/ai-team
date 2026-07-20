## 1. pkg/verdict — контракт

- [x] 1.1 Пакет `pkg/verdict`: типы Verdict/Status, line-anchored парсер `ParseFile`/`Parse`, `FromOutputs(paths)`, `IsNegative()`
- [x] 1.2 Детекция BLOCKED: `ReadBlocked(artifactRoot, feature, agent) (blocked bool, reason string)`
- [x] 1.3 Contract-тесты: канонические строки из промптов/служебной секции распознаются; вердикт в прозе — нет

## 2. pkg/config — новые поля и валидация

- [x] 2.1 Поля `StageTimeout` (глобально), `Timeout`, `OnNegativeVerdict`, `LoopbackTo` (per-agent); yaml-теги, дефолты
- [x] 2.2 `Config.Validate(reg)`: имена агентов существуют, transition/effort/on_negative_verdict/timeout валидны
- [x] 2.3 `Default()` дополнен: `max_retries: 2` у coder, `stage_timeout: 30m`
- [x] 2.4 Сериализация Default через yaml.Marshal в `cmdInit` (гейты сохраняются)

## 3. pkg/runtime — служебная секция, model/effort, логи, таймаут

- [x] 3.1 `buildPrompt`: секция «Служебные требования» (status-файл BLOCKED, stage-summary путь, effort)
- [x] 3.2 Передача `-m <model>` для opencode (model != "" и != auto); warning для неизвестного CLI
- [x] 3.3 Логи: stdout/stderr агента → `.ai-team/logs/{feature}/{agent}.log` + консоль (MultiWriter)
- [x] 3.4 Поля `Model`, `Effort` в `runtime.Agent`; `LogDir` в `Task`

## 4. pkg/pipeline — enforcement и починка механизмов

- [x] 4.1 Декомпозиция Run: `runStage` (execute+checks), `resolveVerdict`, `applyCheckpoints`, `handleLoopback`
- [x] 4.2 Blocked-check до outputs-check; статус blocked в StageResult; остановка с `ErrBlocked`
- [x] 4.3 Verdict-enforcement: негативный вердикт → loopback (interactive) → политика `on_negative_verdict` (default stop); типизированные ошибки для exit-кодов
- [x] 4.4 Loopback: вердикт из реальных выходов; review.md во вход цели; `max_retries` из конфига цели; остановка при исчерпании; фикс `i--` (без повторного запуска reviewer при diff)
- [x] 4.5 Таймаут этапа через context.WithTimeout
- [x] 4.6 Git guard: fail-closed (ошибки git → ошибка этапа), warning без git-репо
- [x] 4.7 Гейт перед deployer: реальная сводка этапов вместо «Все проверки пройдены»
- [x] 4.8 retry-from: валидация выходов пропускаемых агентов (`missing artifacts from previous stage: X`)
- [x] 4.9 Фикс двойного append результата; stage-отчёт при ошибке этапа; rune-safe усечение; финальный отчёт при ctx.Done
- [x] 4.10 DI: `WithRuntimeFactory`, `WithPrompter` для тестируемости; удалить мёртвый код (gate.go-каркас, artifacts var, targetDir)
- [x] 4.11 Recorder-интерфейс (RunStarted/StageStarted/StageFinished/RunFinished) + вызовы из Run

## 5. cmd/ai-team — CLI

- [x] 5.1 Exit-коды 0/1/2/3 по типу ошибки пайплайна
- [x] 5.2 `--help`/`-h`/`help` → usage, код 0; usage дополнен web/--retry-from
- [x] 5.3 Валидация `--feature`; task.md не перезаписывается при `--retry-from`
- [x] 5.4 signal.NotifyContext(SIGINT, SIGTERM)
- [x] 5.5 Запись в SQLite: открыть store, создать run, передать Recorder в пайплайн (ошибки БД — warning, не фатал)
- [x] 5.6 init: yaml.Marshal конфига, .gitignore создаётся при отсутствии (в git-repo), только актуальные каталоги + logs/

## 6. Агенты — промпты и def.yaml

- [x] 6.1 `agents/verifier/def.yaml`: `prompt_file: prompt.md`, `cli: opencode`
- [x] 6.2 reviewer/tester/verifier prompt.md: канонический формат вердикта (line-anchored) явно указан
- [x] 6.3 deployer prompt.md: шаг создания ветки `ai-team/{feature}` при default-ветке; запрет push в default
- [x] 6.4 verifier prompt.md: формат `**Verdict:**` вместо `## Общий вердикт:`

## 7. pkg/eval

- [x] 7.1 Захват stdout судьи + парсинг Score/Comment; ошибка при нераспаршенной оценке
- [x] 7.2 Вызов `opencode run <prompt>` без `--resume`; `cmd.Dir` = target
- [x] 7.3 `cmdEval --feature`: пайплайн из одного агента, оценка его фактических выходов

## 8. pkg/web + frontend

- [x] 8.1 Artifact endpoint: confinement в корень артефактов; убрать CORS `*`; CheckOrigin same-host; graceful shutdown
- [x] 8.2 `/api/pipelines/{id}/artifacts`: реальный список из ФС по feature
- [x] 8.3 Store: колонка verdict (+миграция); StoreRecorder вместо WebNotifier
- [x] 8.4 Frontend: getArtifact как text; WsEvent поля `agent`/`status`; статусы passed/failed/blocked; поллинг активных runs
- [x] 8.5 tsc + vite build проходят

## 9. Тесты и CI

- [x] 9.1 Юнит-тесты Run с фейковым runtime: happy path, REJECTED-стоп, blocked-стоп (exit-ошибки), loopback с review-входом, retry-from валидация, timeout
- [x] 9.2 Contract-тесты verdict (1.3)
- [x] 9.3 e2e: честный rejected-тест (мок: rejected влияет только на reviewer; ассерт остановки после reviewer — test-report.md отсутствует); tester-FAIL тест; blocked-тест (exit 2)
- [x] 9.4 Переписан фиктивный TestAiTeamInit; TestShortenError под rune-safe усечение
- [x] 9.5 CI: gofmt-джоб, frontend-джоб; go vet зелёный
- [x] 9.6 `make build && make test` зелёные; gofmt чистый; go mod tidy

## 10. Документация

- [x] 10.1 README: таблица агентов с verifier, канонический вызов, команда web, exit-коды
- [x] 10.2 AUDIT.md остаётся как исходный список; отметить в нём нечего (аудит-снапшот)
