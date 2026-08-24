# Независимый технический аудит ai-team — 5 августа 2026

> Конвертирован из HTML-версии (оригинал лежал в корне репозитория и не был
> закоммичен). Срез: 5 августа 2026 · ветка
> `agent/implement-human-controlled-cloud-workflow` · ревизия 24ea968.
> Часть находок к текущему моменту уже исправлена — см. баннер в [AUDIT.md](../AUDIT.md)
> и историю PR.

## Вердикт

**Сильное ядро. Незамкнутый cloud-контур.**

Проект уже значительно сильнее обычного AI-agent prototype: переходы, checks,
evidence и delivery контролирует детерминированный Go-код. Но web/cloud
workflow пока не выполняет несколько собственных центральных инвариантов.

Решение: локальный workflow для доверенного Go-проекта можно развивать как
beta. Выпуск cloud/self-hosted режима следует заблокировать до исправления
delivery approval, конкурентности approvals, scheduler semantics, graph resume
и async command admission.

## Карта зрелости

| Область | Оценка |
|---|---|
| Local trusted Go workflow | 7/10 |
| Deterministic architecture | 8/10 |
| Onboarding | 6/10 |
| Documentation coherence | 4/10 |
| Web control plane | 5/10 |
| Cloud operations | 3/10 |
| Testing | 7/10 |
| Maintainability | 5/10 |

## Блокирующие и высокие риски

### P0

**P0-1. Web/cloud не способен завершить delivery approval.**
Перед delivery non-interactive pipeline возвращает `ApprovalRequiredError`,
но не создаёт persisted approval. Web-resume принимает пустой запрос и не
умеет передать plan hash. Повторный Resume снова доходит до того же
checkpoint. Код: pipeline.go:2357, commands.go:180, controller.go:101.
Целевая роль Release Manager описана в CODEX-PLAN.MD:20, но не включена в
delivery approval.
*Исправление:* delivery plan должен создавать обычный approval с
subject=plan hash, ролью release_manager, UI-представлением плана и единым
decision/resume протоколом.
→ *Статус: исправлено в PR #34 (persisted delivery approval с payload).*

**P0-2. Approval store теряет конкурентные решения.**
`Decide` выполняет Load → modify → atomic rename без межпроцессной
сериализации. Два HTTP-запроса могут оба успешно принять конфликтующие
actions либо потерять часть quorum:all. Код: store.go:182, store.go:235.
*Исправление:* SQLite transaction/CAS revision либо межпроцессный lock;
конкурентные тесты для any/all/conflict.
→ *Статус: исправлено в PR #34 (mutex + flock на каталоге run + race-тест).*

### P1

**P1-3. Graph v4 ломает persisted resume кастомного loopback.**
Инвалидация после resume жёстко привязана к action `return_to_coder`, хотя
граф разрешает произвольные имена; условие `> targetIndex+1` оставляет старую
попытку target-stage активной.
→ *Статус: исправлено в PR #34 (семантический backward-detection по индексам графа).*

**P1-4. Scheduler сообщает ложную готовность и ложный успех.**
Scheduler-mode controller создаётся без preflight checker → `Ready: true`.
Worker preflight/config failures выходят с code 1, который poller считает
контролируемым успехом. Archiver failure после успешного execution также
возвращает success при отсутствии run evidence.
*Исправление:* структурированный worker result (completed/waiting/blocked/
business_failed/infra_failed/canceled); readiness через capability/heartbeat.
→ *Статус: частично исправлено в PR #34 (result-line контракт, unknown-ready);
остатки отслеживаются в plans/scheduler-hardening.md.*

**P1-5. Async web-команды могут создавать «призрачные» runs.**
HTTP возвращает run ID до захвата workspace lock; ошибка фонового engine
игнорируется. Окно потерянной отмены в completion/cancel.
→ *Статус: исправлено в PR #34 (синхронный захват lock до 202 + failure sink).*

**P1-6. Delivery фактически поддерживается только для Go.**
README предлагает ручную настройку required check для Rust/Python/Node, но
controller принимает только adapters `command` и `go-test-json`, а generic
command принципиально не признаётся delivery evidence.
*Исправление:* либо Go-only positioning, либо typed adapters pytest/Jest/Cargo.
→ *Статус: открыт; пересекается с plans/adapters-layer.md.*

## Документация и onboarding

Практический smoke build → help → init прошёл. Проблема — рассинхронизация:

| Наблюдение | Влияние |
|---|---|
| README/CONTRIBUTING говорят, что verify не включает gofmt/coverage/E2E; Makefile включает | Контрибьютор не понимает реальный gate → исправлено в PR #34 |
| AUDIT.md описывает live workspace, хотя работает candidate worktree | Главный risk register вводит в заблуждение → добавлен баннер устарелости |
| CODEX-PLAN содержит уже исправленные проблемы как текущие | Roadmap нельзя использовать как источник статуса → добавлен баннер |
| 16 принятых OpenSpec capabilities имеют Purpose TBD | Спеки хуже объясняют назначение → заполнено в PR #34 |
| Frontend README остался шаблоном Vite | Нет инструкции по web-разработке → переписан в PR #34 |

Не хватает: tutorial с ожидаемым выводом, demo repository, screenshots
approval/resume, troubleshooting, upgrade/migration, backup/restore, cleanup,
compatibility policy; LICENSE/changelog/release tags (→ plans/release-basics.md,
plans/onboarding-pack.md).

## Роли, функции и scope

- Семь этапов логически обоснованы, но default требует ~6 human approvals до
  отдельного delivery-подтверждения — слишком дорого. Нужны профили fast /
  standard / regulated (→ plans/workflow-profiles.md, реализовано).
- Внутреннее противоречие: analyst должен задавать вопросы при
  неоднозначности, но runtime запрещает инструмент question (agentcli.go:151).
  Возможен только BLOCKED и новый запуск (→ plans/analyst-question-tool.md,
  реализовано).
- Cloud-компоненты добавлены раньше, чем замкнут основной web→delivery
  vertical. CAS restore существует как библиотека и тест без production-
  потребителя (archive.go:117).
- Текущая ветка меняет 228 файлов (+14 314/−662), хотя собственный roadmap
  требует малых независимых changes.

## Эксплуатационные риски (P2)

**P2-1. Нет retention и garbage collection.** Candidate worktrees создаются
на каждый run без cleanup (manager.go:69); нет политики для runs/logs/events/
CAS blobs/sessions/delivery state.
→ plans/retention-gc.md.

**P2-2. Projection не восстанавливается из source of truth.** Replay/rebuild
SQLite нет; Recorder после первой ошибки отключается навсегда (recorder.go:29);
`ReconcileInterrupted` обновляет все running rows без target/run scope
(sqlite.go:295).
→ Частично исправлено в PR #34 (recorder переживает транзиентные ошибки);
scope reconcile остался, см. plans/web-hardening.md.

**P2-3. Cloud HTTP/session hardening неполон.** Sessions живут в памяти и
чистятся лениво (commands.go:73); HTTP server задаёт только ReadHeaderTimeout
(server.go:171).
→ plans/web-hardening.md.

**P2-4. Approval UI недостаточен для осознанного решения.** UI показывает hash
и названия actions, но не action→target, связанный diff или delivery plan
(PipelineDetail.tsx:123); frontend type отбрасывает поле targets
(types/index.ts:144).
→ Исправлено в PR #34 (payload + targets на карточке approval).

**P2-5. WebSocket cursor не имеет stream identity.** Один sessionStorage
cursor на весь origin; после сброса БД события игнорируются.
→ Исправлено в PR #34 (stream identity + сброс cursor).

## Что сделано хорошо

- Controller-owned transitions: LLM не принимает решения о переходах, checks
  и delivery side effects.
- Candidate isolation: отдельный detached worktree защищает live checkout и
  фиксирует exact baseline identity.
- Exact delivery: plan hash, exact paths/blobs/modes, parent commit, remote
  head, PR verification.
- Fail-closed contracts: verdict markers, mutation scopes, typed Go test
  evidence, zero-test защита исполняются кодом.
- Durable evidence: run/attempt manifests, snapshots, hash-chained event log.
- Честная security boundary: README прямо говорит, что OpenCode permissions
  не являются hermetic OS sandbox.

## Результаты проверок (на срез)

- go test -count=1 ./... — pass; OpenSpec 62/62; git diff --check — pass.
- go vet — pass; govulncheck — чисто; race suite — pass.
- Суммарное Go coverage — 63,9%; ряд web/control handlers имеет 0% function
  coverage.
- Frontend build/lint/test — pass; npm audit: одна moderate PostCSS.
- Cross-compile Windows/Plan9 — ok, runtime CI только Linux.
- Smoke: сборка/help/init в чистом временном Git/Go-проекте прошли.

Пробелы матрицы: нет реального OpenCode/GitHub canary, browser-level E2E,
конкурентных approval-тестов, web delivery round-trip, custom loopback resume,
scheduler fatal-classification test.

## Рекомендуемый порядок исправлений

1. Унифицировать transition и delivery approvals (+ Release Manager
   round-trip, exact plan presentation).
2. Сделать approval decisions транзакционными.
3. Ввести структурированный worker result.
4. Исправить graph resume и invalidation.
5. Исправить async start/cancel admission.
6. Синхронизировать документацию.
7. Выбрать продуктовую границу: Go-only positioning или typed adapters.
8. Добавить operational lifecycle: retention, cleanup, projection rebuild,
   backup/restore, stream identity.
9. Добавить workflow profiles: fast/standard/regulated.
10. Снизить размер change units: разрезать 3060-строчный pipeline.

*Методика: анализ исходного кода и спецификаций, сопоставление заявленных
целей с execution paths, полный project verification gate, coverage review,
cross-compile и onboarding smoke. Тестовые delivery side effects и реальные
внешние PR не выполнялись.*
