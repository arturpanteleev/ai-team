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
| 2026-08-30 | adp-1-codex-adapter | ADP-1: CodexAdapter (codex.go) — headless `codex exec --json`, sandbox workspace-write, CODEX_HOME-изоляция, usage из turn.completed (attested tokens), error taxonomy (auth/model/config/network/sandbox), отмена через процессную группу. Эволюция контракта: Command получает Launch (effort), stubby stdin-prompt для `exec -`, генеральный AI_TEAM_HARNESS_ENV_ALLOW. Offline-тесты mock-бинарником. | review |
| 2026-08-30 | adp-2-claude-code-adapter | ADP-2: ClaudeAdapter (claude.go) — headless `claude -p --output-format json`, `--effort` low/medium/high, `--permission-mode acceptEdits` + deny-list эффектных инструментов (Bash/WebFetch/WebSearch/Task/Skill/LSP), CLAUDE_CONFIG_DIR-изоляция 0700 с settings.json (path-deny секретов), usage из result JSON (total_cost_usd + input/cache/output tokens, attested), ClaudeRunError taxonomy (auth/model/config/network/budget/timeout/permission/runtime). Правка config_test.go (claude более не невалидный CLI). Offline-тесты mock-бинарником `claude`. | review |
| 2026-08-30 | v0-0-prune-runs-export-guard | V0-0: export guard в retention (export.go) — immutable run evidence удаляется `--prune-runs` только при verified-записи `state/exports/<runID>.json` (schema v1, bundle_sha256). Fail-closed: отсутствие/невалидная/non-regular запись → Skipped с причиной и V0-4. Обновлены fixture и CLI-help gc. Тесты: только verified-export prunable, unverified/malformed — пропускаются. | review |

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
| 2 | ADP-1 | P0 | review | adp-1-codex-adapter | Codex adapter (`codex exec`) — зависит от P1-1 |
| 3 | ADP-2 | P0 | review | adp-2-claude-code-adapter | Claude Code adapter (`claude -p`) — зависит от P1-1 |
| 4 | APF-1 | P0 | open | — | Трение approvals: default 1–2 клика |
| 5 | V0-0 | P0 | review | v0-0-prune-runs-export-guard | Безопасность `--prune-runs` до portable export |
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
| 16 | V0-8 | P1 | open | — | Risk-signal measurement (записывать, не маршрутизировать) |
| 17 | V0-9 | P1 | open | — | Agent attribution в Git trailers — после V0-2/V0-3 |
| 18 | P1-4 | P1 | open | — | Containment profile v1 + receipt |
| 19 | P1-9 | P1 | open | — | Contract test: coder не видит будущее ревью |
| 20 | P1-5 | P1 | open | — | Signed bundle через DSSE |
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

- open: 43
- in-progress: 0
- review: 4 (P1-1, ADP-1, ADP-2, V0-0)
- deployed (в рамках спринта): 0
- n/a (требуют владельца/внешних): OPS-1, EXP-1, V0-7 (частично)