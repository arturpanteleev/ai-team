# ai-team — Feature List (ревизия 2, синтез двух ревью)

> **Формат:** `Фича | Краткое описание | Приоритет | Статус | Ссылка`
>
> **Что это за документ.** Ревизия 2 `FEATURES.md` после независимых ревью
> [FEATURES.cloud.md](FEATURES.cloud.md) (взгляд инженерного руководителя, 30.08.2026)
> и [FEATURES.codex.md](FEATURES.codex.md) (арбитраж двух стратегий, 30.08.2026),
> сверенных с фактическим кодом `origin/master` (HEAD `2e48ecc` = волна PR #38–#44 замержена).
>
> **Позиция этой редакции.** Соглашаемся с диа позом обоих ревью:
> 1. У проекта нет проблемы «мало фич» — есть проблема **одной ставки**.
> 2. Доказательство (evidence/attestation) сейчас **нельзя вынести и проверить вне машины** —
>    это главная неиспользованная ценность.
> 3. Дыра **test-tampering** (coder пишет в тесты, а delivery верит их результату) опровергает
>    центральное заявление продукта и ничего не стоит закрыть.
> 4. Собственный harness-descriptor-зоопарк — переизобретение ACP; отказываемся (см. §6).
> 5. Метапроцессный блок 1a (4-мерный вердикт, тиры, compact packets и т.п.) — 6 позиций без
>    наблюдаемой ценности; выкидываем/замораживаем (см. §6).
>
> Отличаемся от рецензентов только в пяти местах (обоснование — [§10](#10-где-мы-не-соглашаемся-с-рецензентами)):
> ACP — спайк, а не готовое решение; `gate` держит LLM-review опцией; report/notifier не удаляем, а замораживаем;
> H0 = вертикальный срез, а не неделя мелких правок; sandbox не «профиль за 2 недели».

---

## Сводка: одна ставка

> **ai-team — локальный policy-and-evidence control plane для AI-изменений:**
> спецификация, которую нельзя проигнорировать; проверки, которые агент не может ослабить;
> переносимая квитанция о том, кто, чем и по какой спецификации сделал этот PR/изменение.

Из этой ставки следуют два трека поставки:

| Трек | Вход | Что делает | Порог входа |
|---|---|---|---|
| **Track V — верификатор (`ai-team gate`)** | готовый diff/PR + спека + checks (чужого агента) | diff-политика, typed checks, ревью опционально, attestation bundle, exit code | один CI-job |
| **Track P — полный конвейер (`ai-team run`)** | задача | analyst→…→deliver с approvals | замена рабочего процесса |

Порядок: **сначала V0** (переносимое доказательство + дешёвый вход), потом ширина P1,
потом signal-gated P2. Полный pipeline остаётся вторым треком, не удаляется.

---

## В. Дорожная карта: горизонты проверяемости

### H0-статус (уже сделано на `origin/master`, волна PR #38–#44)

| Фича | Статус | PR |
|---|---|---|
| Strict JSON-декод в `pkg/strictjson` | ✅ | #37 |
| Workflow profiles fast/standard/regulated | ✅ | #40 |
| Единый tree-hashing + ignore-список | ✅ | #41 |
| Usage-envelope `ai-team usage` | ✅* | #42 |
| Tamper-evident anchor + `ai-team verify` | ✅* | #43 |
| Retention GC `ai-team gc` (dry-run, prune-runs) | ✅* | #44 |
| CI: SHA-pinning, least-privilege, coverage gates | ✅ | #38 |
| License/CHANGELOG/versioned builds | ✅ | #39 |

`*` — готово, но с открытым хвостом: tokens `unknown`, якорь локальный, `verify` только локальный.

**Но** оба ревью справедливо указывают: эти «кросс-секционные» фичи ещё не сделали заявление
продукта *проверяемым снаружи*. `verify` работает только в том же репозитории теми же файлами;
evidence нельзя отправить другому человеку. Это и есть мост к Track V.

---

## 1. P0 / V0 — «переносимое доказательство + дешёвый вход» (первый вертикальный срез)

Цель релиза: человек **без** исходного репозитория и полного pipeline может проверить,
*что именно* проверялось и *кем* создано изменение. Всё ниже — одно ядро `checks + scope +
evidence + verdict + approvals`, без нового pipeline.

| ID | Фича | Описание / definition of done | Зависит от |
|---|---|---|---|
| **V0-1** | **Test-mutation provenance + policy** | Контроллер классифицирует changed paths (`source/tests/meta/infra/generated/unknown`) по каждой стадии. Изменение **существующих** тестов source-стадией — типизированный факт evidence + отдельная строка review + (по умолчанию) отдельное approval. Добавление нового теста ≠ изменение существующего. Policy configurable, результат fail-closed. | — |
| **V0-2** | **Provenance manifest v1** | В attempt/run identity: digests промпта, agent definitион, checks, controller binary, runtime executable/version, заявленные provider/model, base/candidate tree. Неизвестное записывается как `unknown`, не угадывается. Resume детектит drift authority-полей. | — |
| **V0-3** | **Attestation predicate v1** | Версионированная ai-team predicate schema (совместима с in-toto Statement): subject = candidate blob/дерево, predicate = run + spec/config/workflow + checks + mutations + approvals + verdicts + provenance digests. Canonical JSON + golden fixtures + compat policy. Подпись пока опциональна. | V0-1, V0-2 |
| **V0-4** | **`ai-team export` + `verify <bundle>`** | Детерминированный самодостаточный tar: только schema-whitelisted evidence/manifests/attestation (без raw logs/stdout по умолчанию — их digests и typed summaries). `verify bundle.tar` работает без исходного repo/`.ai-team`. Tamper-тесты меняют каждый класс данных и обязаны падать. | V0-3 |
| **V0-5** | **`ai-team gate` MVP (Track V)** | Вход: trusted local base/candidate + спека + check suite. Выход: diff-политика, typed checks, attestation bundle, stable exit codes. **Не** требует OpenCode/runtime; LLM-review — отдельная опция (не по умолчанию). Спека fingerprinted и привязана к bundle. Режим `untrusted` отсутствует до containment (V1-4). | V0-1, V0-4 |
| **V0-6** | **Generic JUnit XML typed adapter** | Строгий bounded parser (zero-test, suite/test counts, failures/errors/skips, raw-output digest). Golden fixtures pytest/Jest/Gradle/Maven. Совместимость заявляется только для реально проверенных fixtures. | — |
| **V0-7** | **Demo + CI action + truthful README** | Публичный demo-repo: `gate → bundle → verify` одним CI-файлом; required check у ai-team самого; README разделяет run/gate/integrity/signed/absence-of-OS-sandbox. Продолжение PR #45. | V0-4, V0-5, V0-6 |

**Definition of done (V0):**
- `verify` на чистой машине проверяет bundle без исходного репозитория;
- одно изменение байта в любом manifest/evidence детектируется;
- вывод отвечает: base/candidate, какие тесты менялись и какой стадией, какие checks реально
  исполнялись, какой runtime/model был заявлен, кто и что одобрил;
- demo подключается одним CI-файлом и проходит на Go + одном JUnit-производящем стеке;
- ≥3 внешних человека запускают demo; ≥1 правит hellано понимает, что bundle доказывает и чего нет.

---

## 2. P1 — «безопасная ширина полного pipeline» (после V0 или конкретного adopter-запроса)

| ID | Фича | Описание / decision gate |
|---|---|---|
| **P1-1** | **RuntimeAdapter contract (тонкий)** | Разделить `launch / session protocol / capabilities / identity / usage / policy mapping`. OpenCode = первая реализация без ослабления текущих гарантий. Unknown capability в strict-профиле блокирует запуск. Контракт без vendor-specific полей. |
| **P1-2** | **ACP feasibility spike** | Один ограниченный спайк: Codex/Claude ACP (headless lifecycle, cwd, file/terminal delegation, cancel, permissions, model identity, usage, error taxonomy) → conformance matrix → решение `ACP / native / hybrid`. Не production-адаптер до проверки. **Если обязательные policy surfaces (mutation scopes, deny, конфиг-изоляция) не enforce — ACP не default.** |
| **P1-3** | Второй verified runtime | Ровно один второй runtime выбранным на P1-2 способом. Offline contract tests + optional live canary. Support level — по evidence, не по декларации. |
| **P1-4** | **Containment profile v1 + receipt** | Linux: bubblewrap+seccomp, сеть deny-by-default через прокси вне песочницы; macOS: `sandbox-exec`. Threat model fs/net/proc/env, cleanup process-tree, fail-closed без backend. Receipt `ENFORCED/PARTIAL/UNAVAILABLE` по осям. До этого — честная формулировка **«controlled, not OS-sandboxed»**. |
| **P1-5** | Signed bundle (DSSE) | DSSE-конверт для V0-3, local/pluggable signer. Verifier разделяет integrity и signer identity. Signing-failure не маскируется unsigned-success в strict policy. |
| **P1-6** | Redaction + retention contract | Классификация полей, secrets-scanner в export, include/exclude policy, retention (настраиваемая) — без «legal compliance» обещаний. Raw logs opt-in. **Блокер экспорта наружу.** |
| **P1-7** | Budget guard + usage truthfulness | Лимиты wall-time/attempts доступны всегда; tokens/cost — только при attested adapter support. Не оценивать по косвенным данным. Budget-reason в evidence. |
| **P1-8** | CI-parity: checks из реального CI проекта | Импорт ограниченного объяснимого набора checks из GitHub Actions/Makefile без исполнения произвольного YAML. Effective suite показывается и fingerprinted до запуска. |

**Definition of done (P1):** один workflow на OpenCode и втором runtime без ослабления
mutation/policy/evidence контрактов; strict containment отказывает при отсутствии backend;
signed bundle проверяется стандартным инструментом и нашим verifier; usage различает
attested/reported/unavailable; ≥1 внешний проект использует `gate` или `run` ≥2 недель.

---

## 3. P2 — развивать только после наблюдаемого сигнала

| ID | Фича | Сигнал для старта |
|---|---|---|
| **P2-1** | Адаптивная глубина по риску (вместо статических профилей) | Есть baseline стоимости/времени по профилям и видно, что static fast/standard/regulated недостаточно |
| **P2-2** | Risk-triggered multi-model review | На реальных runs есть класс high-risk изменений, где второй независимый review ловит пропуски чаще своей стоимости |
| **P2-3** | Параллельный review DAG | P2-2 полезен, но wall-clock review стал bottleneck |
| **P2-4** | OTel GenAI exporter + JSONL SIEM sink | Есть оператор с OTel/SIEM и согласована privacy/redaction; версионировать semconv mapping, не считать genai*-conventions стабильными |
| **P2-5** | Read-only MCP server | Появился второй реальный consumer статуса/evidence кроме встроенного web |
| **P2-6** | Sigstore/Rekor transparency anchoring | Signed bundles реально используются и есть требование независимого timestamp/existence proof |
| **P2-7** | AC → check → evidence trace | Пользователь приносит формальные acceptance criteria и готов поддерживать identifiers |
| **P2-8** | Forge-адаптеры (GitLab/Gitea) / multi-repo | Есть конкретный adopter и его workflow |
| **P2-9** | Compliance evidence pack | Есть regulated design partner, scope assessment и legal/security owner; название не обещает certification |
| **P2-10** | Evidence reader в web | Bundle непонятен через CLI и пользователи явно выбирают web-поверхность |

---

## 4. Прямо сейчас — оставить в покое / не начинать

Полный разбор в [§6](#6) и карта «старое → новое» в [§7](#7). Коротко:

- **Не делать в текущем виде:** harness-descriptor-зоопарк (4 позиции), отдельные Codex+Claude
  адаптеры одновременно, собственный внешний anchor-формат, compact packets под малые окна,
  4-мерный verdict, completion gate, plan-completeness S0/S1/S2, effective-config для каждого
  поля, «EU AI Act compliant» без scope-оценки и юр.ревью.
- **Заморозить (вернуться по сигналу):** durable execution/time-travel (есть lifecycle+evidence;
  при нужде — Temporal/DBOS), context checkpoints, finding→root-cause defect-система, персоны
  reviewer, embeddable библиотека, notifiers, Windows, не-кодовые эффект-адаптеры, dashboard
  time-travel, консолидация ролей 7→5 (адаптивная глубина решит динамически).
- **Сократить поверхность (признаём maintainability 5/10 аудита):** cloud-контур
  (scheduler/CAS/worker) **заморозить развитие**; `pkg/report`/`pkg/notifier` — замораживаемого
  развития, не пакетного удаления (отличаемся от cloud-ревью — см. §10); specs pruning 62→~10.

---

## 5. Решения владельца (16 → закрыто дефолтами, по согласованию с обладателем)

Из `docs/plans-open-questions.md` (16 вопросов) большинство закрыто дефолтами, поддерживающими
стратегию этого документа:

| № | Вопрос | Решение |
|---|---|---|
| 1 | Coverage-полы | Расти естественно; точечно для safety-критичных |
| 2 | Dependabot для SHA-pin | Да (готово в #38-контексте) |
| 3 | Лицензия | Apache-2.0 ✅ (#39) |
| 4 | Первый тег | v0.1.0 поставить ✅ (#39) |
| 5 | Fast: project-local override reviewer | Разрешить project-local (готово в #40) |
| 6 | Fast без verifier | Да (осознанная жертва для fast) |
| 7 | Regulated quorum all | Оставить (это и есть смысл regulated) |
| 8 | Hash-ignore-list в конфиг | Да, вынести |
| 9 | Usage-токены сейчас | Ждать attested (P1-7), не парсить stdout |
| 10 | Метрики в web | Позже (P2-10/OTel), CLI и bundle достаточно |
| 11 | Локальный якорь | Принять промежуточно; заменить на V0-3 attestation |
| 12 | Автопроверка anchor при resume | Да, fail-closed |
| 13 | GC-дефолты 720h/20 | Ок |
| 14 | Auto-GC после прогона | Опция конфига, не дефолт |
| 15 | `--prune-runs` без экспорта | Убрать до появления V0-4 export |
| 16 | Branch protection master | Заполнить required checks (часть V0-7) |

---

## 6. ВЫКИНУТЬ / ЗАМОРОЗИТЬ (из FEATURES.md v1 по итогам ревью)

### 6.1 Выкинуть — обесценено рынком / стандартом

| ID (v1/BACKLOG) | Что | Почему | Вместо |
|---|---|---|---|
| `A2` / `[P0] Harness descriptor` | Свой контракт адаптера: descriptor+capability+launch+usage+event_bridge | Переизобретение ACP (клиент↔агент JSON-RPC, реестр, 25+ агентов) | один **ACP-клиент** (§P1-2) |
| `A3`, `A4` / Codex, Claude adapters | Отдельные `codex exec` / `claude -p` адаптеры | ровно то, что делают `codex-acp` / `claude-agent-acp`; вечный долг за версии | P1-2, P1-3 |
| `C2` / `[P2] External anchoring` | Свой append-only store / подпись | существующий стандарт проверяется чужим инструментом | **V0-3 + P2-6** |
| `E5` / Durable execution / time-travel | свой checkpoint+replay «как LangGraph» | есть lifecycle+evidence+resume; при нужде — Temporal/DBOS | — |
| `G12` / Time-travel в дашборде | просмотр state по timestamp в web | производное от E5; GitHub Agent HQ выигрывает дистрибуцией | ссылка на bundle (V0-4) |

### 6.2 Выкинуть — «метапроцесс», не наблюдаемый пользователем (блок 1a v1)

| ID (v1) | Что | Почему |
|---|---|---|
| `F2` / 4-мерный verdict + routing | 4 оси, коды причин, детерм. next-action | `APPROVED/CHANGES_REQUESTED/PASS/FAIL/BLOCKED` достаточно; 4 оси меняют только внутреннее описание |
| `E9` / Completion gate | финиш-вектор STOP/CONTINUE/… | терминальные состояния графа уже есть и работают |
| `E10` / Plan-completeness S0/S1/S2 | тиры authority до исполнения | валидация конфига/графа до LLM уже fail-closed |
| `G3` / Small-model packets | окна 4k/8k/16k, fail-closed overflow | контекст — не дефицит 2026; оптимизация под уходящее ограничение |
| `G2` / Effective-config explanation | winningSource/planConstraint на поле | pre-optimization для 4 слоёв и одного пользователя |
| `E6` / Context checkpoints | typed-снапшот при компакции | компакция — ответственность харнеса, не оркестратора |
| `F3` / Evidence-integrity finding→root-cause→fix-impact | defect-менеджмент поверх оркестратора | отдельный продукт; недопустимая ставка при нуле пользователей |

### 6.3 Заморозить до конкретного запроса

| ID | Что | Условие разморозки |
|---|---|---|
| `G7`/`H3` | Встраиваемая библиотека/пакет | внешний проект явно просит встроить контроллер |
| `D4` | Windows support | пользователь на Windows; пока честно «macOS/Linux» |
| `D3` | Не-кодовые эффект-адаптеры (deploy/ticket/migration) | Track P доказал ценность на коде |
| `F9` | Персона-мышление reviewer | данные, что качество ревью упирается в когнитивные ловушки |
| `G5` | Progress bridge | второй потребитель статуса; вероятно закроется MCP (P2-5) |
| `G8` | Notifiers (Slack/email) | появилась команда, а не один человек |
| `OPS8` | Консолидация ролей 7→5 | адаптивная глубина сделает динамически |
| `E1`,`E2`,`E4` | Soft-timeout, circuit breaker, gitignore-гард | примитивы автономных 24/7; мы human-gated. Держать как мелкие чиненки (P3) |
| `E3` | Edge-политика «обязан ship»/анти-зацикливание | `max_visits` покрывает; возврат при реальном зацикливании |
| `E7`,`E8` | Process-observability, worktree receipts | поглощаются P1-4 (sandbox receipt) и V0-3 (attestation) |
| `F6` | Evals calibrated corpus | advisory-сигнал без корпуса; не вкладываться |
| Cloud-контур | scheduler/CAS/worker | поддерживать существующее, не расширять до adopter |

### 6.4 Сократить существующую поверхность

| Что | Действие |
|---|---|
| `pkg/report` | заморозить развитие (не пакетное удаление — см. §10) |
| `pkg/notifier` (кроме console) | свернуть до интерфейса, потребителей нет |
| `pkg/eval` | оставить advisory, не развивать |
| 62 OpenSpec capabilities | сократить до ~одного набора на инструмент (`OPS2`) |
| Cloud-контур | заморозить развитие (см. 6.3) |

---

## 7. Карта «старое → новое» (чтобы ничего не потерялось молча)

| Старый ID | Старый приоритет | Новый |
|---|---|---|
| `A1` Harness-абстракция runtime | P0 | **P1-1** (тонкий RuntimeAdapter; обоснование — риск блокировки OpenCode, не рост) |
| `A2` Harness descriptor | P0 | **выкинуть** → ACP (P1-2) |
| `A3`,`A4` Codex/Claude адаптеры | P1 | **выкинуть** → ACP-спайк + один второй runtime (P1-2/P1-3) |
| `B1` Онбординг-история | P0 | **V0-7** (demo-repo + CI + verify), не скриншоты |
| `B2` README к коду | P0 | **V0-7** (продолжение PR #45) |
| `B3` Публичный репозиторий | P0 | **V0-7** (demo-repo) |
| `G1` Убить трение approvals | P0/P1 | **P2-1** (адаптивная глубина; профили #40 уже снизили) |
| `C1` OS-sandbox | P1 | **P1-4** (containment profile + receipt; расширенная threat-model работа) |
| `C1b` Честное позиционирование | P1 | `controlled, not OS-sandboxed` до P1-4; часть V0-7/P1-4 |
| `C3` Log redaction | P1 | **P1-6** (блокер экспорта) |
| `C5` Branch protection | P0/P1 | **V0-7/P1-8** (заполнить required checks) |
| `C2` External anchoring | P2 | **выкинуть** → V0-3 + P2-6 |
| `D1a` Один не-Go адаптер | P1 | **V0-6** (JUnit XML как generic) |
| `D1b`,`D1c` 2-й/3-й адаптер | P2/P3 | поглощены V0-6 |
| `D2` Forge-адаптеры | P2 | **P2-8** |
| `D5` Multi-repo | P2 | **P2-8** |
| `D6` Model routing | P2/P3 | **P2** (после ACP — тонкая настройка) |
| `VER` verify за 10 сек | P0/P1 | **V0-4** (export + verify bundle) |
| `MCP` | P1 | **P2-5** (по сигналу второго consumer) |
| `E1` Soft-timeout | P1 | **P3** (заморожено) |
| `E2` Circuit breaker | P1 | **P3** |
| `E3` Анти-зацикливание | P1 | **P3** (`max_visits`) |
| `E4` Gitignore-гард | P1 | **P2** (дёшево; частично в diff-классификации V0-1) |
| `E5` Durable execution | P2 | **выкинуть** |
| `E6` Context checkpoints | P2 | **выкинуть** |
| `E7` Process observability | P2 | поглощено **P1-4** |
| `E8` Worktree receipts | P2 | поглощено **V0-3** |
| `E9` Completion gate | P1 | **выкинуть** |
| `E10` Plan-completeness тиры | P2 | **выкинуть** |
| `F1` Multi-model review | P2 | **P2-2** (по риск-сигналу) |
| `F2` 4-мерный verdict | P2 | **выкинуть** |
| `F3` Evidence-integrity цепочка | P1 | **выкинуть** (parking lot) |
| `F4` AC trace | P2 | **P2-7** (сохранён — единственный из блока с внешней ценностью) |
| `F5` Prompt fingerprinting | P3 | **V0-2** (provenance manifest) — самый большой скачок |
| `F6` Evals corpus | P2 | **заморожено** |
| `F7` Runtime binary/model pinning | P2 | поглощено **V0-2** |
| `F8` Data/control separation | P2 | **P2** (доделать) |
| `F9` Персона reviewer | P3 | **заморожено** |
| `G2` Effective-config | P2 | **выкинуть** |
| `G3` Small-model packets | P3 | **выкинуть** |
| `G5` Progress bridge | P3 | **заморожено** |
| `G6` Dry-run | P1 | **P1** scope note: dry-run выходит в V0-5 bundle-печати; полноценный — P2-1 context |
| `G7` Встраиваемая библиотека | P2 | **заморожено** |
| `G8` Notifiers | P3 | **заморожено** |
| `G9` Task templates | P2 | **P2** (после V0-5 тонкий слой) |
| `G10` Параллельный DAG | P2 | **P2-3** |
| `G11` Дашборд — readers evidence | P1 | **P3** (P2-10): link на bundle главный |
| `G12` Time-travel dashboard | P2 | **выкинуть** |
| `G13` Budget guard | P2 | **P1-7** (поднят) |
| `OPS1` OpenSpec errata | P2 | **P3** |
| `OPS2` Specs pruning | P2 | **P2** (сохранён) |
| `OPS3` Поверхность pruning | P2 | **P1** (см. 6.4) |
| `OPS4` Structured logging | P2 | **P2** (предусловие P2-4 OTel) |
| `OPS5` Pipeline refactoring | P2 | **P2** (инкрементально по ходу V0/P1) |
| `OPS6` Write-only evidence | P2 | **P3** |
| `OPS7` Web/scheduler hardening | P2 | **P3** (cloud заморожен) |
| `OPS8` Роли 7→5 | P2 | **заморожено** |
| `OPS9` Backup/restore, glossary | P2 | **P2-9** (compliance pack часть) |
| Usage/токены unknown | P1 | **P1-7** (attested, не stdout-парсинг) |
| Fast-профиль со steering | P1 | заменён **P2-1** (адаптивная глубина) |
| Метрики/obs | P3 | **P2-4** (OTel+SIEM по сигналу) |

**Итог по числам:** из ~55 позиций двух старых документов — 14 выкинуть, 12 заморозить,
9 поглощены, 13 добавлено новоемого. Рабочий бэклог: **~19 позиций в V0/P1/P2** в одной оси.

---

## 8. Метрики успеха и критерии остановки

| Горизонт | Метрика успеха | Срок | Если не достигли |
|---|---|---|---|
| **V0** | Посторонний человек `verify bundle.tar` на своей машине и правильно пересказывает, что доказано и что нет | 3–4 недели | Проблема в формулировке ценности → переписать историю, не код |
| **V0** | ≥3 внешних человека запустили demo; ≥1 правит умения понимает guarantees+gaps | 4 недели | Порог входа высок → упростить до одного бинарника без harness |
| **P1/Track V** | ≥1 команда регулярно прогоняет `gate` в CI ≥2 недель | 8 недель | **Track V не сработал** — главный кандидат разворота к Track P как личному инструменту |
| **P1** | Прогон на не-Go репо доходит до delivery | 8 недель | JUnit XML недостаточен → нативный адаптер под конкретный стек |
| **P1** | Один workflow на двух runtime без ослабления контрактов | 8 недель | ACP не еnforce surfaces → hybrid/native |
| **P2-1** | Медианная стоимость фичи упала ≥2× после адаптивной глубины | после данных | Риск-сигнал плохо разделяет → упростить |
| **P2-9** | ≥1 разговор с командой с регуляторным требованием | 16 недель | Регуляторная гипотеза не подтвердилась → снять из позиционирования |

**Критерий полной остановки Track V:** за 8 недель после релиза `gate` им не воспользовался
никто, кроме автора → гипотеза «рынку нужны доказательства» опровергнута этим экспериментом;
возврат к Track P как personal tool. Лучше признать по заранее объявленному критерию.

---

## 9. Риски этой стратегии (честно)

1. **EU AI Act (Art. 11/12/26(6)) — хорошо обоснованная ниша, но узкая.** Обычная разработка
   не подпадает под high-risk. Поэтому используем *только* как канал discovery и формулировку
   «audit-ready evidence», НЕ заявление «compliant» без юр.ревью.
2. **`gate` может быть фичей, а не продуктом** (Agent HQ добавит agentic review сам). Контраргумент:
   они добавят ревью, а не переносимую квитанцию с провенансом и containment-receipt. Сигнал — §8.
3. **ACP может не покрыть требования ai-team** (mutation scopes, deny-политики, изоляция конфига,
   usage/cost не в обязательных полях v1). Решается P1-2 спайком с заранее заданной fail-matrix.
4. **Портал в bundle без подписи доказывает только целостность набора.** UI/docs обязаны разделять
   integrity / authenticity / external existence. Это заложено в V0-3/V0-4/P1-5.
5. **Классификация тестовых путей эвристична между языками.** `unknown` — видимый класс,
   policy проектно расширяема (V0-1).
6. **Сокращение surface** (report/notifier/cloud) — обратимо на одном уровне: заморозка, не удаление.
7. **Цены/проценты веб-ресурсов неточны.** Направление (верификация = бутылочкоё горлышко, тесты
   геймятся) подтверждено несколькими работами; конкретные числа — дрейфуют, строить на них
   решения сильнее «порядка величины» нельзя.

---

## 10. Где мы не соглашаемся с рецензентами (и почему)

| Расхождение | cloud | codex | Позиция этой редакции |
|---|---|---|---|
| ACP как замена RT-contract | «один ACP-клиент — решение» | «ACP — транспорт; тонкий RuntimeAdapter остаётся» | **codex** (+ спайк P1-2 до решения) |
| `gate` и LLM-review | ревью в составе gate | «без runtime; LLM-review опцией, untrusted до containment» | **codex** (MVP = deterministic checks/policy/attestation) |
| report/notifier удаление | удалить | «не пакетом, сначала telemetry» | **codex**: заморозить развитие, не удалять; решение после данных |
| H0 как «неделя мелких правок» | 2–3 недели, всё маленькое | «самостоятельный вертикальный срез» | **codex**: V0 — реальный срез ~4 недели (schema+bundle+gate+JUnit+demo) |
| Стоимость sandbox | «1–2 недели на профиль» | «threat model + cross-platform + policy + cleanup — отдельная security-работа» | **codex**: P1-4 с полноценным scope |
| Usage через ACP | «побочный эффект ACP» | «v1 schema без обязательных usage/cost — не закроется сам» | **codex**: P1-7 (attested) |
| EU AI Act | «дифференциатор стал требованием» | «канал discovery, не текущая гарантия» | **codex**: гипотеза для интервью, «audit-ready evidence», не «compliant» |
| OTel/SIEM приоритет | P1 | P2 by signal | **codex** (P2-4) |

В остальном соглашаемся с обоими ревью и их диагнозом: одна проверяемая ставка > каталога идей.

---

*Приложение: все утверждения о коде этой редакции проверены по `origin/master` 2e48ecc
(§4 FEATURES.cloud.md Appendix B, §10 FEATURES.codex.md). Внешние ссылки обоих ревью —
в их источниках. Документ-предшественник — `FEATURES.md` v1; бэклог-источник — `BACKLOG.md`.*