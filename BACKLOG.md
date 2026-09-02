# ai-team — Мастер-бэклог (сводный)

> **Что это:** единый сквозной бэклог, сводящий ВСЕ источники состояния проекта в один документ:
> `AUDIT.md` (аудит 2026-07-20), `CODEX-PLAN.MD` (roadmap P0–P6 + OpenSpec-волны 0–4),
> `docs/audits/2026-08-05-technical-audit.md` (свежайший независимый аудит), `docs/plans-open-questions.md`
> (16 открытых решений владельца), `FEATURES.md` (текущий feature-list), README/F1–F20 (что реально
> shipped), `openspec/project.md` и три исследования рынка (`ai-team-market-15.html`,
> `ai-team-vs-auto-company-compare.html`, `ai-team-alpha-free-result.html`).
>
> **Статус каждой фичи (по вашему запросу):**
> - `⭕ open` — не начато / можно брать
> - `🟡 in-progress` — в работе
> - `✅ ready` — готово, есть PR (закрыт и замержен или открыт)
> - `⛔ blocked` — заблокировано другим решением/решением владельца
>
> **Соглашения по источникам:** `[AC]` = MaxMiksa/Auto-Company, `[AU7]` = аудит 2026-07-20 (AUDIT.md),
> `[AU8]` = тех.аудит 2026-08-05, `[CP]` = CODEX-PLAN.MD, `[FEAT]` = FEATURES.md, `[PLAN#N]` = пункт
> плана 1–38 из базы знаний, `[RQ#N]` = открытый вопрос владельца из plans-open-questions.md,
> `[M15]` = ai-team-market-15.html, `[ACcmp]` = ai-team-vs-auto-company-compare.html.
>
> *Примечание: файла `cloud.md` в корне нет — cloud-контекст собран из `openspec/`-archives
> (cloud-auth-and-rbac, cloud-worker-execution) и доков планирования.*

---

## TL;DR — рекомендованный порядок (первый спринт)

Согласовано с независимым продуктовым анализом. Проект — **«контроллер доверия над агентами»**:
сильнейшая инженерная база, но ⭐ 0 и ⭐ трение (~6 approvals/фича). До первых 3–5 реальных
пользователей не раздувать инженерию. Первые 2–3 недели:

1. **Публичный малый релиз на готовой базе** — README к коду, quickstart, demo-repo, release-заметки (P0).
2. **Убить трение approvals** — default-профиль до 1–2 кликов (P0).
3. **Harness-абстракция + ОДИН второй адаптер** (Codex ИЛИ Claude Code) — снять «это только OpenCode» (P0).
4. **Один массовый не-Go test-адаптер** (pytest ИЛИ Jest) — применимость не в чисто-Go-мире (P1).
5. **Доказательный артефакт «verify за 10 секунд»** — продающая история дифференциатора (P0/P1).
6. Параллельно — честная формулировка «controlled, not sandboxed, roadmap: sandbox»; решить H2
   в пользу публичной истории; дать mission/why.

---

## Раздел 0. Открытые продуктовые решения (СНАЧАЛА — решить, потом кодить)

| ID | Решение | Варианты | Рекомендация | Статус |
|---|---|---|---|---|
| H1 | Ниша | (а) «контроллер доверия» (доказуемая доставка) 🌟 (б) «автономный агент-инженер» (OpenHands-стиль) | **а)** — единственное, за что реально платят; «б» требует sandbox и размывает дифференциатор `[M15][ACcmp]` | ⭕ open |
| H2 | Приоритет публичной истории vs тех.долг | (а) сперва публичность 🌟 (б) сперва закрыть долг | **а)** — при ⭐ 0 история важнее любой инженерии `[M15 B.6]` | ⭕ open |
| H3 | Встраиваемая библиотека vs CLI | (а) остаться CLI 🌟 (б) пакет/библиотека | **а)** для первого релиза; «б» — после подтверждения аудитории | ⭕ open |
| H4 | Adapter-модель vs Go-native | (а) принимать adapter-контракт 🌟 (б) остаться Go-native | **а)** — абстракция даёт гибкость без миграции; дёшево `[FEAT OTHER]` | ⭕ open |
| H5 | Business goal (mission/why) | добавить goal-state / остаться инженерным | **добавить 1–2 предложения** — усиливает рекрутинг и продукт | ⭕ open |
| RQ1 | Coverage-полы: поднимать агрессивно? | агрессивно / расти естественно | естественно; дописывать точечно для safety-критичных | ⭕ open `[RQ#1]` |
| RQ2 | Dependabot для SHA-пинов actions? | да / нет | да (автоматизация) | ⭕ open `[RQ#2]` |
| RQ3 | Лицензия Apache-2.0 vs MIT | Apache-2.0 / MIT | Apache-2.0 (уже выбрано; устраивает) | ✅ ready `[RQ#3][PR39]` |
| RQ4 | Первый git-тег v0.1.0 | поставить после мержа | поставить (уже поставлен 2026-08-23) | ✅ ready `[RQ#4]` |
| RQ5 | Fast-профиль: project-local override reviewer | разрешить / встроенный 2-й агент | разрешить project-local | ⭕ open `[RQ#5]` |
| RQ6 | Fast убирает verifier (совмещает reviewer) | жертва независимости | осознанная; ок для fast | ⭕ open `[RQ#6]` |
| RQ7 | Regulated quorum all — не слишком жёстко? | ок / смягчить | оставить; это и есть regulated | ⭕ open `[RQ#7]` |
| RQ8 | Hash-ignore-list вынести в конфиг? | захардкоден / конфиг | конфиг (RQ#8) | ⭕ open `[RQ#8]` |
| RQ9 | Парсить usage-токены сейчас или ждать adapters | сейчас (хрупко) / ждать | ждать adapters-layer `[RQ#9]` | ⭕ open `[RQ#9]` |
| RQ10 | Метрики: вкладка в web? | только CLI / +web | +web (позже, P2) | ⭕ open `[RQ#10]` |
| RQ11 | Локальный якорь — принять промежуточно? | принять / требовать внешний | принять промежуточно, внешний — P2 `[RQ#11]` | ⭕ open `[RQ#11]` |
| RQ12 | Автопроверка якоря при resume (fail-closed)? | авто / ручная | авто — да `[RQ#12]` | ⭕ open `[RQ#12]` |
| RQ13 | GC-дефолты 720h/20 устраивают? | ок / другой | ок, подтвердить | ⭕ open `[RQ#13]` |
| RQ14 | Auto-GC после прогона? | ручной / опция | опция конфига `[RQ#14]` | ⭕ open `[RQ#14]` |
| RQ15 | `--prune-runs` опасен — убрать до экспорта? | убрать / оставить | убрать до появления архивации `[RQ#15]` | ⭕ open `[RQ#15]` |
| RQ16 | Защита ветки master (была пуста) — перепроверить | — | проверить GitHub settings | ⭕ open `[AU8]` |

---

## Раздел 1. УЖЕ СДЕЛАНО — база (✅ ready)

Всё ниже реально замержено и работает. Это «что уже есть» — не планировать заново.

### 1.1 Ядро цикла (ready)

| Фича | Что | Источник | PR/Статус |
|---|---|---|---|
| init + строгий config v4 | bootstrap `.ai-team/config.yaml`, `.git/info/exclude`, авто-Go-check | F1 `[AU8]` | ✅ ready |
| Pipeline orchestration | RunEngine, граф, loopback non-TTY, `--resume`, max_visits | F2 | ✅ ready |
| Workflow graph v4 | outcome-рёбра, approval policies, max_visits, валидация до LLM, fail-closed | F3 `[CP]` | ✅ ready |
| Human approvals + RBAC | exact-SHA-256 subject, actor/role/action/comment, quorum any/all, stale-reject, flock | F4 `[AU8 #34]` | ✅ ready |
| Delivery: canonical plan + 2-фазное подтверждение | 7 гейтов, exact commit/push/PR, crash-safe | F5 | ✅ ready |
| Candidate worktree | detached worktree от baseline, live не мутируется, stale у инвалидации | F6 `[AU7 C-02 done]` | ✅ ready |
| Deterministic checks | argv-без-shell, timeout, process-group kill, bounded output, go-test-json | F7 | ✅ ready |
| Mutation scopes + baseline guard | per-agent globs, запрет выхода скоупа/.git/.ai-team | F8 | ✅ ready |
| Verdict-контракт + BLOCKED | один канон-маркер, fail-closed, exit 2 | F9 | ✅ ready |
| Evidence + tamper-evident + verify | immutable run/attempt, hash-chain events, anchor.json, `ai-team verify` | F10 `[PR43]` | ✅ ready |
| Usage-метрики (`ai-team usage`) | duration/attempts/loopback/outcome | F11 `[PR42]` | ✅ ready* |
| Retention GC | candidate worktree cleanup, `--prune-runs`, dry-run | F12 `[PR44]` | ✅ ready* |
| Профили fast/standard/regulated | генерируют явный config, не «магия» | F13 `[PR40]` | ✅ ready |
| Evals (advisory) | независимый LLM-eval артефактов в temp-dir | F14 | ✅ ready* |
| Web dashboard + cloud mode | SQLite+WebSocket+React, auth session/CSRF/HMAC, worker, scheduler, CAS | F15–16 | ✅ ready |
| Layered agent registry | project→plugin→user→built-in | F17 | ✅ ready |

`*` — ready с открытым хвостом (TokensUnknown, одноразовые дефолты), см. Раздел 0.

### 1.2 Уже реализованные планы (✅ ready)

| # | План | PR | Статус |
|---|---|---|---|
| 1 | Analyst question tool (interactive TTY) | PR #36 | ✅ ready |
| 2 | Workflow profiles | PR #40 | ✅ ready |
| 3 | Metrics & usage | PR #42 | ✅ ready |
| 6 | Retention & GC | PR #44 | ✅ ready |
| 18 | Tree hashing perf | PR #41 | ✅ ready |
| 20 | Strict-decode helper | PR #37 | ✅ ready |
| 27 | Event log anchoring | PR #43 | ✅ ready |
| 32 | CI supply chain | PR #38 | ✅ ready |
| 33 | Release basics | PR #39 | ✅ ready |
| C-02 | Isolated candidate worktree | 91e4fda | ✅ ready |
| P0–P3 | Большинство roadmap `[CP]` | 91e4fda + волны | ✅ ready |
| P0-1/P0-2, P1-3/4/5, P2-4/5 | Тех.аудит fixes | PR #34 | ✅ ready |

### 1.3 В работе / частично готово

| ID | Фича | Что | Источник | Статус |
|---|---|---|---|---|
| DOC | README отстаёт от кода (usage/verify/gc/profiles) | синхронизировать README с reality CLI | FEAT FIX, `[AU8]` | 🟡 in-progress (PR #45 open) |

---

## Раздел 2. ФИЧИ — ближайший спринт (делать первыми)

Согласовано с продуктовым анализом: закрывают барьеры тракции (невидимость, трение, «только OpenCode»,
«только Go») и нельзя-потерять (дифференциатор).

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| B2 | **P0** | README к коду | quickstart выполняется буквально; usage/verify/gc/profiles | `[AU7][AU8][M15]` | 🟡 in-progress | #45 |
| B1 | **P0** | Онбординг-история наружу | quickstart, скриншот-сценарии, demo-repo, release-заметки, troubleshooting | `[AU8] #34`, `[M15]` | ⭕ open | — |
| B3 | **P0** | Публичный репозиторий/присутствие | чтобы рынок узнал о нише («0 звёзд») | `[M15 B.6]` | ⭕ open | — |
| A1 | **P0** | Harness-абстракция runtime | вынести привязку из `agentcli.go:25`; поддержка OpenCode/Codex/Claude Code через абстракцию | `[FEAT 0]` | ⭕ open | — |
| A2 | **P0** | Harness descriptor контракт | descriptor + capability manifest + launch_profile + usage_normalizer + event bridge; fail-closed инспекция | `[FEAT 0]` | ⭕ open | — |
| G1 | **P0/P1** | Убить трение approvals (default 1–2 клика) | сгруппировать/одноразовое одобрение прежних этапов при delivery; fast-путь; осмысленные дефолты | `[AC] fast`-steering, `[FEAT FIX Дорогие гейты]` | ⭕ open | — |
| A3 | **P1** | Codex adapter (ОДИН из двух) | `codex exec` + usage/результат + изоляция env | `[FEAT]` | ⭕ open | — |
| A4 | **P1** | Claude Code adapter (ОДИН из двух) | `claude -p` + JSON-результат + total_cost_usd | `[FEAT]` | ⭕ open | — |
| D1a | **P1** | Один массовый не-Go test-адаптер (pytest ИЛИ Jest) | typed adapter, доказательство discovery/pass/fail/zero-test | `[AU7 H-07][AU8 P1-6]` | ⭕ open | — |
| VER | **P0/P1** | «Verify за 10 секунд» (продающий артефакт) | канонизировать UX `ai-team verify`, человекочитаемый вывод, добавить в demo-repo/CI | `[AU8]` | ⭕ open | — |
| MCP | **P1** | MCP server (read-only статус) | внешние агенты/инструменты читают статус ai-team | `[PLAN#14][M15][ACcmp]` | ⭕ open | — |

---

## Раздел 3. Безопасность и доверие (закрыть C-01/H-04; часть — после тракции)

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| C1 | **P1** (аккуратно, не блокируя тракцию) | OS-sandbox | docker/bwrap изоляция FS/net/proc/env; sandbox-receipt; строгий профиль fail-closed; снимает запрет «strict/secure» | `[AU7 C-01][AU8][M15 1][PLAN#36]` | ⭕ open | — |
| C1b | **P1** | Честное позиционирование «controlled, not sandboxed» | убрать/переформулировать запрещённую рамку до sandbox; roadmap-позиция | `[AU7][FEAT P0 FIX]` | ⭕ open | — |
| C2 | **P2** | External anchoring evidence (H-04) | подпись/внешний append-only store; terminal manifest root-hash; replay idempotent | `[AU7 H-04][AU8]` | ⭕ open | — |
| C3 | **P1** | Log redaction / retention policy | классификация чувствительных данных, redaction, retention, безопасный export | `[AU7 M-07][PLAN#5]` | ⭕ open | — |
| C4 | **P2** | CI supply chain / per-package coverage | per-package thresholds по safety-критичным (pipeline/delivery/evidence/checks/safeio/runtime) | `[AU7 M-03][PR38 base]` | ⭕ open | — |
| C5 | **P0/P1** | Weak spot: branch protection master пуст | проверить/заполнить required checks | `[AU8]` | ⭕ open | — |

---

## Раздел 4. Универсализация / расширение платформы (после тракции)

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| D1b | **P2** | Второй test-адаптер (из оставшихся) | pytest/Jest/Cargo | `[AU7 H-07]` | ⭕ open | — |
| D1c | **P3** | Третий test-адаптер | — | `[AU7 H-07]` | ⭕ open | — |
| D2 | **P2** | Forge-адаптеры delivery (GitLab/Gitea/bare-remote) | расширение за GitHub | `[CP P6][PLAN#17]` | ⭕ open | — |
| D3 | **P3** | Не-кодовые эффект-адаптеры | deploy Cloudflare, ticket, DB migration; plan→approve→execute→verify→compensate | `[AU7 H-10]` | ⭕ open | — |
| D4 | **P3** | Windows support | кросс-платформа (Runtime уже Unix/Windows/plan9) | `[PLAN#35]` | ⭕ open | — |
| D5 | **P2** | Multi-repo (одна задача, неск. репо) | — | `[PLAN#37]` | ⭕ open | — |
| D6 | **P2/P3** | Multi-model routing по ролям (provider-neutral) | классы моделей без имён в ядре, host-профиль, escalation | `[FEAT 1a]` | ⭕ open | — |

---

## Раздел 5. Надёжность исполнения (по одной — по мере реальной боли)

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| E1 | **P1** | Soft-timeout с сохранением прогресса | убит по таймауту, но есть валидный evidence → partial success | `[AC]` `[M15]` `[FEAT]` | ⭕ open | — |
| E2 | **P1** | Circuit breaker + usage-limit backoff | N ошибок → cooldown; 429/quota/billing → длинный backoff | `[AC][M15]` `[FEAT]` | ⭕ open | — |
| E3 | **P1** | Edge-политика «обязан ship» / анти-зацикливание | GO/NO-GO; N повторов → escalate/fail; дополняет max_visits | `[AC][M15][ACcmp]` `[FEAT]` | ⭕ open | — |
| E4 | **P1** | Gitignore/мета-файл гард | snapshot мета-файлов до, restore после | `[AC][M15]` `[FEAT]` | ⭕ open | — |
| E5 | **P2** | Durable execution / time-travel | переживание падений, replay узла | `[LangGraph][M15]` `[FEAT]` | ⭕ open | — |
| E6 | **P2** | Context checkpoints / compaction recovery | компактный typed-снапшот при компакции, restore без authority | `[FEAT 1a][PLAN#12]` | ⭕ open | — |
| E7 | **P2** | Process-execution observability (ресурсные receipts) | monotonic elapsed, CPU/mem/процессы, cleanup-доказательство | `[FEAT 1a]` | ⭕ open | — |
| E8 | **P2** | Worktree-isolation receipts | bounded attempt workspace + write-back receipt | `[FEAT 1a]` | ⭕ open | — |
| E9 | **P1** | Completion gate | STOP/CONTINUE/ESCALATE/SPLIT/FOLLOW_UP; finalize fail-closed | `[FEAT 1a]` | ⭕ open | — |
| E10 | **P2** | Plan-completeness пре-аудит гейт (SDD-тиры) | минимальная authority S0/S1/S2 до исполнения | `[FEAT 1a]` | ⭕ open | — |

---

## Раздел 6. Качество evidence / review (дифференциатор — нельзя потерять)

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| F3 | **P1** | Evidence-integrity цепочка | finding→root-cause→fix-impact→regression→hash-chain→proof; стабильный findingId | `[FEAT 1a]` | ⭕ open | — |
| F4 | **P2** | AC trace (машинная трасса acceptance criteria) | AC→design→task→test→evidence→verdict; referential integrity | `[AU7 H-03][AU8][PLAN#25]` | ⭕ open | — |
| F1 | **P2** | Multi-model review с кворумом | N независимых host+model, синтез+quorum, opt-in | `[FEAT 1a][PLAN#11]` | ⭕ open | — |
| F2 | **P2** | 4-мерный verdict + routing | fit/quality/evidence/risk + детерм. next action, fail-closed | `[FEAT 1a]` | ⭕ open | — |
| F5 | **P3** | Prompt fingerprinting | hash промптов/host в manifest, drift detection | `[PLAN#26]` | ⭕ open | — |
| F6 | **P2** | Evals calibrated corpus | versioned tasks/golden outcomes/regression thresholds | `[AU7 M-02][PLAN#4]` | ⭕ open | — |
| F7 | **P2** | Runtime binary/model/provider pinning | resolve+hash бинарника раз на run, версия/probe, запрет смены внутри run | `[AU7 H-08]` | ⭕ open | — |
| F8 | **P2** | H-09 data/control separation в артефактах | typed inputs, data delimiters, запрет tool-instructions | `[AU7 H-09]` | ⭕ open | — |
| F9 | **P3** | Персона-мышление для reviewer | чек-лист когнитивных ловушек (Munger), opt-in | `[AC][M15][ACcmp]` | ⭕ open | — |

---

## Раздел 7. Конфигурация / продуктовость / UX

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| G2 | **P2** | Effective-configuration объяснение | winningSource/planConstraint, fail-closed lineage | `[FEAT 1a]` | ⭕ open | — |
| G3 | **P3** | Small-model packets / compact context | output contract, fail-closed overflow | `[FEAT 1a]` | ⭕ open | — |
| G5 | **P3** | Progress bridge (lifecycle-status контракт) | публичный контракт статуса | `[PLAN#15]` | ⭕ open | — |
| G6 | **P1** | Dry-run (план без исполнения/затрат) | разблокирует первые шаги, онбординг | `[PLAN#16][PE]` | ⭕ open | — |
| G7 | **P2** | Встраиваемая библиотека/пакет (СМ. H3) | API-поверхность пакета | `[FEAT POLISH][M15 B.6]` | ⭕ open | — |
| G8 | **P3** | Notifiers (Slack/email) | подписки | `[PLAN#13][AC]` | ⭕ open | — |
| G9 | **P2** | Task templates (workflow presets) | переиспользуемые пресеты | `[PLAN#10]` | ⭕ open | — |
| G10 | **P2** | Параллельный DAG (конкурентные независимые узлы) | — | `[PLAN#38][CP P2]` | ⭕ open | — |
| G11 | **P2** | Дашборд — читатели evidence + verify в UI | навигация по immutable evidence, verify в web | `[FEAT POLISH]` | ⭕ open | — |
| G12 | **P2** | Time-travel в дашборде | просмотр state по timestamp | `[FEAT POLISH]` | ⭕ open | — |
| G13 | **P2** | Budget guard (token/time лимиты, fail-closed) | — | `[PLAN#9]` | ⭕ open | — |

---

## Раздел 8. Управление продуктом / процесс (open)

| ID | Приор. | Фича | Что | Источник | Статус | PR |
|---|---|---|---|---|---|---|
| OPS1 | **P2** | OpenSpec errata (append-only archive) | архив не редактировать; errata с conformance | `[AU7 M-01][PLAN#31]` | ⭕ open | — |
| OPS2 | **P2** | Specs pruning (62→~10) | сокращение капабилити до одного opsx-набора на инструмент | `[PLAN#30]` | ⭕ open | — |
| OPS3 | **P2** | Поверхность pruning (pkg/report, notifier, дубль-UI) | удалить дубли только при продуктовом решении + данных использования | `[CP P5][PLAN#22]` | ⭕ open | — |
| OPS4 | **P2** | Structured logging (slog + `--quiet/--json`) | — | `[PLAN#24]` | ⭕ open | — |
| OPS5 | **P2** | Pipeline refactoring (3060-строчный) | reduce change units | `[AU8 #10][PLAN#19]` | ⭕ open | — |
| OPS6 | **P2** | Write-only evidence: добавить readers или убрать | ControllerIdentity/snapshots | `[PLAN#23]` | ⭕ open | — |
| OPS7 | **P2** | Web hardening + scheduler hardening + session janitor | Secure cookie, scoped reconcile, readiness/heartbeat | `[AU8]` `[PLAN#28][#29]` | ⭕ open | — |
| OPS8 | **P2** | Grid: консолидация AI-ролей 7→5 | tester/verifier→reviewer после checks | `[PLAN#21][CP §9]` | ⭕ open | — |
| OPS9 | **P2** | Backup/restore, migration/compat policy, glossary | часть онбординг-пака | `[AU8 #34]` | ⭕ open | — |

---

## Раздел 9. Отложенные / НЕ начинать (осознанно)

| ID | Приор. | Что | Почему отложено | Источник |
|---|---|---|---|---|
| — | ⛔ | Собственный SandboxBackend как отдельная тяжёлая платформа | cloud-изоляция через disposable workers; standalone sandbox — только при подтверждённом рынке | `[CP §9][PE]` |
| — | ⛔ | Расширение платформ/деплоев до полного e2e human-controlled workflow | после стабилизации | `[CP §9][CP P6]` |
| — | ⛔ | Пакетное удаление report/notifier/eval | каждому — отдельное продуктовое решение + данные использования | `[CP P5]` |
| — | ⏸ | strict-sandbox-and-candidate-isolation change | superseded; candidate-worktree отдельно, sandbox отдельно | `[CP волна 2]` |

---

## Сводка по состоянию

| Статус | Кол-во (оч.) | Комментарий |
|---|---|---|
| ✅ ready | ~30 | Сильная замерженная база: ядро цикла, graph v4, approvals+RBAC, delivery 7 гейтов, evidence+verify, профили, web/cloud, 9 планов #1–33 |
| 🟡 in-progress | 2 | README→код (PR #45 open), + 16 владельческих решений (RQ) |
| ⭕ open | ~55 | фичи ниже — см. разделы 2–8 |
| ⛔ blocked/отложено | ~5 | осознанно отложенные |

**Главный риск сейчас:** не раздувать инженерию (группы A3–A8 целиком, E5/E6/E7/E8, F-blok,
G3/G7/G10) до первых реальных пользователей. Дифференциатор «контроллер-owned + exact-SHA + verify»
— не терять ни в коем случае.

---

*Документ сгенерирован для ревью. Открытые решения (Раздел 0) — приоритет перед кодингом.*
