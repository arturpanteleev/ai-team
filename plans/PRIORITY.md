# Priority (порядок выполнения бэклога)

Статусы задач: `idea` → `in-progress` → `PR` (+ссылка) → `done` (после мерджа).
Порядок ниже — стартовый, владелец может менять строки местами.

| # | Фича | Файл | Суть | Статус |
|---|------|------|------|--------|
| 1 | Analyst question tool | plans/analyst-question-tool.md | Разрешить аналитику задавать вопросы (сейчас tool denied) | **PR** [#36](https://github.com/arturpanteleev/ai-team/pull/36) |
| 2 | Workflow profiles | plans/workflow-profiles.md | Пресеты fast/standard/regulated одним полем конфига | **PR** [#40](https://github.com/arturpanteleev/ai-team/pull/40) |
| 3 | Metrics & usage | plans/metrics-and-usage.md | Мерить токены/время/ретраи/исходы на каждом этапе | **PR** [#42](https://github.com/arturpanteleev/ai-team/pull/42) |
| 4 | Evals harness | plans/evals-harness.md | Golden traces + негативные сценарии + метрики протокола | idea |
| 5 | Log redaction | plans/log-redaction.md | Редакция секретов в логах/отчётах/экспортах | idea |
| 6 | Retention & GC | plans/retention-gc.md | Политика хранения worktrees/runs/CAS/sessions, ai-team gc | **PR** [#44](https://github.com/arturpanteleev/ai-team/pull/44) |
| 7 | Adapters layer | plans/adapters-layer.md | Provider-neutral runtime: адаптеры CLI-хостов | idea |
| 8 | Model routing | plans/model-routing.md | Классы моделей и профиль этап→модель | idea |
| 9 | Budget guard | plans/budget-guard.md | Лимиты токенов/времени на run, fail-closed при превышении | idea |
| 10 | Task templates | plans/task-templates.md | Workflow-пресеты одной командой + spike/analytics | idea |
| 11 | Review mesh | plans/review-mesh.md | Панель ревьюеров с ролями, синтез, quorum | idea |
| 12 | Context checkpoints | plans/context-checkpoints.md | Типизированный event log + чекпоинты + кросс-рановая память | idea |
| 13 | Notifiers | plans/notifiers.md | Подписка на события: Slack + email первой итерацией | idea |
| 14 | MCP server | plans/mcp-server.md | Read-only MCP: статус ранов для внешних агентов | idea |
| 15 | Progress bridge | plans/progress-bridge.md | Публичный контракт статуса lifecycle + support levels | idea |
| 16 | Dry-run | plans/dry-run.md | Планирование прогона без исполнения и трат | idea |
| 17 | Forge adapters | plans/forge-adapters.md | Delivery: GitHub/GitLab/Gitea/bare-remote | idea |
| 18 | Tree hashing perf | plans/tree-hashing-performance.md | Убрать ~17 полных проходов хеширования за прогон | **PR** [#41](https://github.com/arturpanteleev/ai-team/pull/41) |
| 19 | Pipeline refactoring | plans/pipeline-refactoring.md | Доразрезать runStage и RunWithResult | idea |
| 20 | Strict-decode helper | plans/strict-decode-helper.md | Один строгий JSON-парсер вместо трёх копий | **PR** [#37](https://github.com/arturpanteleev/ai-team/pull/37) |
| 21 | Role consolidation | plans/role-consolidation.md | AI-роли 7→5: tester/verifier → reviewer после checks | idea |
| 22 | Surface pruning | plans/surface-pruning.md | Удалить pkg/report и дублирующий консольный UI | idea |
| 23 | Write-only evidence | plans/write-only-evidence.md | ControllerIdentity/snapshots: дать читателей или убрать | idea |
| 24 | Structured logging | plans/structured-logging.md | slog + --quiet/--json вместо fmt.Printf | idea |
| 25 | AC trace | plans/acceptance-criteria-trace.md | Машинная трасса acceptance criteria → evidence | idea |
| 26 | Prompt fingerprinting | plans/prompt-fingerprinting.md | Хеши промптов/хоста в attempt manifest, drift detection | idea |
| 27 | Event log anchoring | plans/event-log-anchoring.md | Root hash терминального манифеста + внешний якорь | **PR** [#43](https://github.com/arturpanteleev/ai-team/pull/43) |
| 28 | Web hardening | plans/web-hardening.md | Sessions janitor, Secure cookie, scoped reconcile | idea |
| 29 | Scheduler hardening | plans/scheduler-hardening.md | Typed enqueue conflicts, archive retry, cancellable cancel | idea |
| 30 | Specs pruning | plans/specs-pruning.md | Спеки 62→~10, одна копия opsx на инструмент | idea |
| 31 | OpenSpec errata | plans/openspec-errata.md | Архив append-only, исправления через errata | idea |
| 32 | CI supply chain | plans/ci-supply-chain.md | Pin actions по SHA, permissions, per-package coverage | **PR** [#38](https://github.com/arturpanteleev/ai-team/pull/38) |
| 33 | Release basics | plans/release-basics.md | LICENSE, CHANGELOG, теги, сборка бинарников | **PR** [#39](https://github.com/arturpanteleev/ai-team/pull/39) |
| 34 | Onboarding pack | plans/onboarding-pack.md | Tutorial с ожидаемым выводом, демо-репо, troubleshooting | idea |
| 35 | Windows support | plans/windows-support.md | Заявить поддержку или честно ограничить macOS/Linux | idea |
| 36 | Sandbox | plans/sandbox.md | OS-containment: fs/network/process/env receipts | idea |
| 37 | Multi-repo | plans/multi-repo.md | Одна задача — несколько репозиториев | idea |
| 38 | Parallel DAG | plans/parallel-dag.md | Конкурентное исполнение независимых узлов графа | idea |
