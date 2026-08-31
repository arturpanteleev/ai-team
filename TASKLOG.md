# ai-team — TASKLOG (статус-лог исполнения бэклога)

Источник задач: `ai-team-backlog.html` (канонический плоский список, №1–№63).
Канонический статус доски — в `ai-team-backlog.html`; этот файл — рабочий лог
«что сделано / что не сделано / что в процессе», ведётся контроллером.

Статусы задач (совпадают с доской):
- `open` — не начато
- `in-progress` — в работе (активная ветка/код)
- `review` — передан на ревью (ветка запушена, правки от внешнего ревьюера включены или ожидаются)
- `deployed` — merged / проведено операционно
- `n/a` — задача не-кодовая или требует внешних действий владельца; фиксируется, но не закрывается агентом

Задача №7 `DOC-45` (PR #45, архив аудитов) — уже в Review отдельным PR, в этом спринte не переделывается.

---

## Sprint-лог

| Дата | Ветка | Что сделано | Итог |
|---|---|---|---|
| 2026-08-30 | backlog-sprint | Базовая линия: чистая ветка от origin/master 2e48ecc, бэклог-доки и статус-лог внесены в git | baseline |
| 2026-08-30 | p1-1-runtime-adapter-contract | P1-1: контракт RuntimeAdapter (adapter.go: Name/Describe/Validate/Command/Environment + Capability + Registry), OpenCodeAdapter (opencode.go), AgentCLIRuntime — тонкий оркестратор (agentcli.go), валидация CLI в config/agent/preflight через registry, eval через адаптеры. Все `go test ./...` зелёные, build/vet/gofmt чисто. | review |
| 2026-08-31 | v0-9-agent-attribution-trailers | V0-9: agent attribution через Git trailers. Deferred (post-terminal) delivery: внутри run delivery-стадия делает только `delivery.Prepare` + `authorizeDelivery` (event `delivery_deferred`, marker `{StatePath, PlanHash}`), git-история НЕ меняется до terminal finalize. После finalize при outcome completed post-terminal хук (`pkg/pipeline/delivery_deferred.go`) перегружает canonical plan (сверка plan.Hash с маркером и approvedPlanHash), читает attestation digest + runtime identity = sha256(run.json.Provenance) и исполняет реальный controller.Execute с controller-derived trailers `ai-team-run`/`ai-team-runtime`/`ai-team-attestation`. `pkg/delivery`: Request.Trailers, ValidateTrailers, trailers персистятся в state ДО commit, commit message = FullCommitMessage(plan.CommitMessage, trailers), verifyCommittedChange сверяет trailers (crash-recovery/resume idempotent). Tamper-evident terminal record `{RunDir}/delivery.json` (TerminalRecord v1 + self-integrity record_sha256, строгая валидация, однократная запись, idempotent retry). `evidence.FindDelivered` сначала читает delivery.json, fallback — attempt manifests. Сбой post-terminal хука повторяем: CLI `ai-team deliver --run <id> [--target] [--feature]` (run обязан быть terminal completed, plan обязан совпасть с delivery_deferred event). Go-тесты: trailer-формат/commit-with-trailers/crash-recovery, TerminalRecord roundtrip+tamper (вкл. несогласованные trailers), deferred flow через explicit approval (fake controller вызван ровно 1 раз post-terminal, record+trailers сверены), DeliverDeferred guards. E2E TestE2E_SuccessfulPipeline зелёный (approve → deferred commit на origin-ветке, live HEAD не тронут). | review |

---

## Порядок и зависимости

Крупные P0-цепочки ведутся последовательно (один рабочий поток):
- **Runtime-цепочка:** P1-1 → ADP-1 → ADP-2 (ADP зависят от контракта P1-1).
- **V0-аттестация:** V0-2 → V0-3 → V0-4 (provenance → predicate → export/verify); V0-1 независим, но полагается на V0-2 semantics.
- **V0-гейт:** V0-1/V0-4 → V0-5; V0-6 независим; V0-7 — после V0-4/V0-5/V0-6.
- **V0-9:** после стабильных полей V0-2/V0-3.
- **OPS-4:** после V0-0/V0-4 (retention не должен уничтожать единственную копию evidence).

Мелкие независимые задачи (OPS-10, OPS-1, V0-0 и т.п.) закрываются по ходу между крупными.

---

## Список задач

| № | ID | Приор. | Статус | Ветка/PR | Заметки |
|---|---|---|---|---|---|
| 1 | P1-1 | P0 | review | p1-1-runtime-adapter-contract | Тонкий RuntimeAdapter contract; блокирует ADP-1/ADP-2 |
| 2 | ADP-1 | P0 | open | — | Codex adapter (`codex exec`) — зависит от P1-1 |
| 3 | ADP-2 | P0 | open | — | Claude Code adapter (`claude -p`) — зависит от P1-1 |
| 4 | APF-1 | P0 | open | — | Трение approvals: default 1–2 клика |
| 5 | V0-0 | P0 | open | — | Безопасность `--prune-runs` до portable export |
| 6 | OPS-1 | P0 | open | — | git tag v0.1.0 + GitHub Release (нужен owner-доступ) |
| 7 | DOC-45 | P0 | deployed | PR #45 | Уже в Review/отдельный PR; HMR-рывок не в спринte |
| 8 | V0-1 | P0 | open | — | Test-mutation provenance + policy |
| 9 | V0-2 | P0 | open | — | Provenance manifest v1 |
| 10 | V0-3 | P0 | open | — | Attestation predicate v1 — зависит от V0-1/V0-2 |
| 11 | V0-4 | P0 | open | — | `ai-team export` + `verify <bundle>` — зависит от V0-3 |
| 12 | V0-5 | P0 | open | — | `ai-team gate` MVP — зависит от V0-1/V0-4 |
| 13 | V0-6 | P0 | open | — | Generic JUnit XML typed adapter |
| 14 | V0-7 | P0 | open | — | Demo + CI action + truthful README — после V0-4/5/6; нужны внешние люди |
| 15 | EXP-1 | P1 | open | — | Track V kill-watch + интервью (не-кодовая) |
| 16 | V0-8 | P1 | review | v0-8-risk-signals | Risk-signal measurement (записывать, не маршрутизировать) |
| 17 | V0-9 | P1 | review | v0-9-agent-attribution-trailers | Agent attribution в Git trailers (deferred post-terminal delivery) — после V0-2/V0-3 |
| 18 | P1-4 | P1 | review | p1-4-containment-profile-v1 | Containment profile v1 + receipt (OS-sandbox backend — V2) |
| 19 | P1-9 | P1 | review | p1-9-coder-stage-independence | Contract test: coder не видит будущее ревью |
| 20 | P1-5 | P1 | review | p15-dsse-signed-bundle | Signed bundle через DSSE (ed25519, stdlib-only) |
| 21 | P1-6 | P1 | open | — | Redaction + retention contract |
| 22 | P1-7 | P1 | open | — | Budget guard + usage truthfulness |
| 23 | P1-8 | P1 | open | — | CI-parity check import |
| 24 | OPS-8 | P1 | open | — | Data/control separation remainder |
| 25 | OPS-3 | P1 | open | — | Fail-closed evidence verification при resume |
| 26 | OPS-2 | P1 | open | — | Configurable tree-hash ignore list |
| 27 | OPS-10 | P1 | open | — | Dependabot для SHA-pinned Actions |
| 28 | OPS-6 | P1 | open | — | Structured logging (slog + quiet/json) |
| 29 | P1-2 | P2 | open | — | ACP feasibility spike (research/conformance) |
| 30 | P2-13 | P2 | open | — | Meta-file guard |
| 31 | P2-1 | P2 | open | — | Adaptive workflow depth (signal-gated) |
| 32 | P2-2 | P2 | open | — | Risk-triggered multi-model review (signal-gated) |
| 33 | P2-3 | P2 | open | — | Параллельный review DAG (signal-gated) |
| 34 | P2-4 | P2 | open | — | OTel GenAI exporter + JSONL SIEM sink (signal-gated) |
| 35 | P2-5 | P2 | open | — | Read-only MCP server (signal-gated) |
| 36 | P2-6 | P2 | open | — | Sigstore/Rekor anchoring (signal-gated) |
| 37 | P2-7 | P2 | open | — | AC trace (signal-gated) |
| 38 | P2-8a | P2 | open | — | Forge adapters (signal-gated) |
| 39 | P2-8b | P2 | open | — | Multi-repo workflow (signal-gated) |
| 40 | P2-9 | P2 | open | — | Compliance evidence pack (signal-gated) |
| 41 | P2-10 | P2 | open | — | Evidence reader в web (signal-gated) |
| 42 | P2-11 | P2 | open | — | Task templates (signal-gated) |
| 43 | P2-12 | P2 | open | — | Provider-neutral model routing (signal-gated) |
| 44 | P2-14 | P2 | open | — | Полный dry-run + cost estimate (signal-gated) |
| 45 | OPS-4 | P2 | open | — | Auto-GC opt-in — после V0-0/V0-4 |
| 46 | OPS-5 | P2 | open | — | OpenSpec pruning |
| 47 | OPS-7 | P2 | open | — | Incremental pipeline refactoring |
| 48 | OPS-9 | P2 | open | — | Freeze policy report/notifier/eval/cloud |

---

## Резюме на текущий момент

- open: 46
- in-progress: 0
- review: 1 (P1-1)
- deployed (в рамках спринта): 0
- n/a (требуют владельца/внешних): OPS-1, EXP-1, V0-7 (частично)