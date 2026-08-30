# ai-team — разбор двух feature-стратегий и backlog Codex

> **Дата среза:** 30 августа 2026.
>
> **Что сравнивается:** [FEATURES.md](FEATURES.md) и
> [FEATURES.cloud.md](FEATURES.cloud.md).
>
> **Кодовая база для проверки:** `origin/master` на commit `2e48ecc`. Текущий
> checkout находится на более старом commit `d852197`, поэтому факты о уже
> реализованных `usage`, `verify`, `gc` и профилях проверялись по
> `origin/master`, а не только по рабочему дереву.
>
> **Назначение документа:** не объединить все идеи, а выбрать следующую
> проверяемую ставку проекта, явно зафиксировать разногласия двух версий и
> оставить backlog, который реально можно исполнять последовательно.

---

## 0. Итоговая позиция

Обе версии видят настоящие проблемы, но оптимизируют разные вещи:

- `FEATURES.md` оптимизирует **архитектурную полноту control plane**;
- `FEATURES.cloud.md` оптимизирует **внешнюю проверяемость и шанс получить
  пользователей**.

Моя рекомендация ближе ко второй, но не совпадает с ней полностью.

**Главная ставка:** превратить существующее evidence-ядро в переносимый
`verify/gate`-контур, который полезен независимо от того, каким агентом создан
код. Полный семистадийный pipeline остаётся вторым режимом, а не удаляется.

Целевая формулировка продукта:

> **ai-team — локальный policy-and-evidence control plane для AI-изменений:
> он связывает спецификацию, точный diff, исполненные проверки, решения
> людей и provenance агента в проверяемую квитанцию.**

Из этого следуют четыре решения:

1. Сначала сделать доказательство **переносимым и понятным**, потом углублять
   внутреннюю метамодель.
2. Добавить `ai-team gate` для проверки уже готового изменения; `ai-team run`
   остаётся полным управляемым процессом генерации и доставки.
3. Не строить пять самописных harness-интеграций, но и не объявлять ACP полной
   заменой внутреннего runtime-контракта. ACP должен пройти ограниченный спайк.
4. Не продавать «compliance» или «secure sandbox», пока соответствующие
   границы не доказаны реализацией и внешней проверкой.

---

## 1. Что написал каждый документ

### 1.1. `FEATURES.md`

Основная логика документа:

- главный блокер роста — жёсткая привязка к OpenCode;
- поэтому P0 — multi-harness runtime и собственный harness descriptor;
- после абстракции разблокируются Codex/Claude adapters, usage, model routing,
  multi-model review и compact packets;
- параллельно закрываются OS-sandbox, MCP, дорогие approvals, один Go-only
  typed adapter и несколько reliability-проблем;
- сильная сторона проекта усиливается подробными evidence/verdict/process
  контрактами.

Сильные стороны:

- хорошо знает фактическую архитектуру и называет конкретные места изменения;
- правильно замечает OpenCode lock-in, отсутствие OS sandbox, `TokensUnknown`
  и Go-only typed evidence;
- видит зависимости между runtime, model routing, usage и review;
- последовательно защищает controller-owned execution и fail-closed семантику;
- содержит много качественных идей для зрелой регулируемой системы.

Слабые стороны:

- это скорее каталог архитектурных возможностей, чем последовательность ставок;
- P0/P1 перегружены, а одна проблема иногда заведена дважды: например,
  OS-sandbox одновременно `NEW` и `FIX`;
- свой harness descriptor выбран до проверки существующего стандарта;
- блок 1a смешивает ближайшую продуктовую ценность и позднюю governance-модель;
- нет exit criteria, метрик принятия и правила остановки несыгравшей ставки;
- статусная сводка непрозрачна: например, комментарий к P0 упоминает README,
  хотя в таблице README имеет P1;
- «архитектурно разблокирует многое» используется как замена доказательству,
  что пользователю это нужно сейчас.

### 1.2. `FEATURES.cloud.md`

Основная логика документа:

- проблема проекта — не нехватка функций, а отсутствие сфокусированной ставки;
- multi-harness является гигиеной, а не дифференциатором;
- дифференциатор — tamper-evident evidence и контролируемые человеческие решения;
- evidence пока нельзя нормально вынести и проверить независимо;
- существует необработанный риск изменения тестов implementer-агентом;
- вход в полный pipeline слишком дорог, поэтому нужен отдельный Track V:
  `ai-team gate` для проверки чужого PR без запуска полного процесса;
- backlog надо сократить, часть поздних метапроцессных функций удалить или
  заморозить, а успех каждого горизонта измерять.

Сильные стороны:

- начинает с продуктового тезиса и цены первого контакта;
- вводит горизонты, определения готовности, метрики и stop conditions;
- отличает дифференциатор от обязательной инфраструктурной гигиены;
- находит конкретную дыру: `coder` имеет `allowed_paths: ['**']`, а результаты
  тестов используются как delivery evidence;
- предлагает переносимый bundle, provenance fingerprint и стандартную
  attestation вместо дальнейшего усложнения локального `anchor.json`;
- не боится явно замораживать и поглощать позиции.

Слабые стороны:

- некоторые внешние факты превращены в решения без промежуточного спайка;
- ACP переоценён: он стандартизирует client↔agent transport, но не заменяет
  controller-owned launch/isolation/policy/identity контракт;
- стабильная ACP v1 schema не содержит обязательных `usage` или `cost` полей,
  поэтому `TokensUnknown` не закрывается «как побочный эффект ACP»;
- `ai-team gate` заявлен «без harness», но независимое LLM-review всё равно
  требует runtime; корректный MVP без runtime может делать deterministic
  checks/policy/attestation, а LLM-review должен быть отдельной опцией;
- sandbox оценён слишком оптимистично: использовать известный backend дешевле,
  чем писать свой, но threat model, cross-platform enforcement, network policy,
  cleanup и receipt всё ещё являются отдельной security-работой;
- EU AI Act даёт перспективный сегмент, но не доказывает, что именно этот
  продукт уже имеет покупателя; это гипотеза для интервью, не основание для
  заявления «compliant»;
- предложение удалить готовые `report/notifier` преждевременно без данных об
  использовании; разумнее заморозить развитие и удалить позже при наличии
  миграционного выигрыша;
- H0 назван набором маленьких изменений, хотя schema, portable bundle,
  signing и совместимый verifier образуют самостоятельный вертикальный срез.

---

## 2. Сравнение по единым критериям

Оценка по шкале 1–5 — не «кто победил», а где какой документ полезнее как
вход в итоговую стратегию.

| Критерий | `FEATURES.md` | `FEATURES.cloud.md` | Вывод Codex |
|---|---:|---:|---|
| Связь с текущим кодом | 4 | 4 | Оба знают код; cloud точнее выделяет test-tamper и portability |
| Ясность продуктового тезиса | 2 | 5 | У cloud есть ответ «зачем следующий релиз» |
| Дисциплина приоритетов | 2 | 4 | Горизонты и заморозка лучше длинного P0–P3-каталога |
| Архитектурная глубина | 5 | 3 | Первый документ сильнее как источник поздних механизмов |
| Реализм стандартов/интеграций | 3 | 3 | Первый переизобретает контракт; второй переоценивает ACP |
| Безопасность формулировок | 4 | 3 | Первый осторожнее; второй недооценивает sandbox и compliance-риск |
| Наблюдаемая ценность пользователю | 2 | 5 | Bundle/gate/demo можно понять до принятия полного workflow |
| Метрики и stop conditions | 1 | 5 | В первом документе почти отсутствуют |
| Реализуемость одним автором | 2 | 4 | Cloud лучше сокращает scope, но всё ещё переоценивает H0 |
| Работа с неопределённостью | 2 | 3 | Нужны явные spikes и decision gates в обоих вариантах |

### Что беру из `FEATURES.md`

- необходимость убрать OpenCode lock-in;
- controller-owned runtime contract и fail-closed capability checks;
- OS containment как обязательную границу строгого режима;
- provider-neutral model routing как поздний слой;
- multi-model review только для задач, где дополнительная независимость
  оправдывает стоимость;
- MCP как будущий интеграционный интерфейс;
- идеи evidence integrity, process receipts и plan completeness — в parking lot,
  откуда их можно доставать при появлении конкретного требования.

### Что беру из `FEATURES.cloud.md`

- приоритет переносимого evidence;
- Track V / `ai-team gate` как дешёвый вход в продукт;
- test-mutation disclosure и explicit approval;
- provenance fingerprint;
- in-toto/DSSE/Sigstore как направление совместимости;
- generic typed test adapter;
- измеримые горизонты и stop conditions;
- заморозку расширения cloud/UI до появления пользователей.

### Что не принимаю ни у одного документа

- «главная фича = архитектурный слой» без пользовательского эксперимента;
- «стандарт автоматически закрывает внутренний контракт»;
- крупные security/compliance-заявления, основанные только на наличии логов;
- оценки сроков без dependency graph и явного definition of done;
- удаление работающей поверхности только ради сокращения числа пакетов;
- превращение всех хороших идей в обязательства текущего релиза.

---

## 3. Арбитраж ключевых разногласий

### 3.1. Multi-harness: P0 или не нужен?

Оба крайних ответа неверны.

Фактически `Runtime` interface уже существует, но `AgentCLIRuntime` и
`AgentCLIArgs` знают только OpenCode, а isolation полностью опирается на
`OPENCODE_*`. Значит, lock-in реален. При этом ACP действительно предоставляет
JSON-RPC client↔agent protocol, capability negotiation, permission requests,
filesystem и terminal surfaces; существуют `codex-acp` и `claude-agent-acp`.

Но ACP не определяет все требования ai-team:

- как безопасно найти и запустить конкретный executable;
- какой env разрешён процессу;
- как привязать модель, agent definition и prompt к evidence;
- как сопоставить ACP permission с controller policy;
- как гарантированно получить usage/cost;
- какой support level подтверждён conformance-тестом.

**Решение:** оставить тонкий внутренний `RuntimeAdapter` contract, где ACP —
одна реализация transport/session protocol, а OpenCode — текущая native
реализация. Не писать отдельный богатый descriptor на каждый CLI до спайка.

### 3.2. Test-tamper guard: настоящий P0, но не blanket ban

В коде подтверждено:

- `coder` может менять любой путь;
- контроллер уже фиксирует `AttemptManifest.Mutations` по стадиям;
- candidate evidence содержит changed files, patch digest и checks;
- delivery требует typed test evidence для точного workspace digest.

Следовательно, недостаёт не данных, а policy interpretation. Полный запрет
изменения тестов кодером сломает нормальные bugfix/refactoring-сценарии и плохо
обобщается на разные языки.

**Решение:** классифицировать пути, записывать `test_changes_by_stage`, выводить
этот факт в review/bundle и по умолчанию требовать отдельное approval, если
source-стадия меняла существующие тесты или удаляла/ослабляла проверки.
Добавление нового теста и изменение существующего теста должны быть различимы.

### 3.3. Portable evidence: P0, но integrity ≠ authenticity

Текущий `anchor.json` проверяет внутреннюю hash-chain и digests manifests, но
лежит рядом с проверяемыми данными. Он выявляет случайное повреждение и
несогласованное редактирование, но владелец каталога может пересобрать и цепь,
и anchor.

Нужно разделить три уровня:

1. **Portable integrity:** самодостаточный bundle проверяется без исходного
   `.ai-team` и без репозитория.
2. **Signed authenticity:** DSSE-подпись связывает attestation с identity
   подписанта.
3. **External existence/anchoring:** transparency log или доверенное внешнее
   хранилище доказывает, что запись существовала вне локальной машины.

in-toto Statement подходит как внешний envelope для subject + custom
predicate ai-team. Он не заменяет внутренние manifests и replay; он ссылается
на их digests.

### 3.4. `ai-team gate`: отдельный режим, а не новый второй продукт

`gate` должен использовать то же ядро `checks + scope + evidence + verdict`,
а не создавать параллельную реализацию.

MVP:

- принимает base/candidate, спецификацию и check suite;
- классифицирует diff и test mutations;
- выполняет deterministic checks только в явно заявленном trusted режиме;
- выпускает bundle и exit code;
- не требует OpenCode и не делает LLM-review по умолчанию.

Следующая версия может добавить независимое LLM-review через тот же runtime
adapter. Режим для недоверенного PR нельзя заявлять до containment.

### 3.5. Sandbox: нужен, но это не «быстрый wrapper»

Готовые backends уменьшают объём кода, однако продуктовая гарантия состоит не
в названии `bubblewrap`/Seatbelt/container, а в доказанном профиле:

- filesystem boundary;
- network boundary;
- process tree и cleanup;
- environment/credentials;
- platform support и fail-closed fallback;
- receipt, который сообщает `ENFORCED`, `PARTIAL` или `UNAVAILABLE` по каждой
  оси.

До этого корректная формулировка остаётся: **controlled, not OS-sandboxed**.

### 3.6. EU AI Act: канал discovery, не текущая гарантия

Официальный текст действительно задаёт logging/record-keeping обязанности для
high-risk AI systems и минимум шесть месяцев хранения определённых логов.
Но обычный coding workflow не становится high-risk system автоматически, а
наличие evidence не равно соответствию всему регламенту.

**Решение:** использовать «audit-ready evidence» как гипотезу для интервью с
регулируемыми командами. Не использовать «EU AI Act compliant» без scope
assessment, retention/redaction, operational controls и юридического review.

---

## 4. Целевая продуктовая модель

```text
готовый diff / PR ──> ai-team gate ──┐
                                    ├─> policy + typed checks + evidence
задача ──> ai-team run ──> runtime ─┘               │
                                                    v
                                      portable attestation bundle
                                                    │
                                      verify / CI / external archive
```

Общее ядро для `gate` и `run`:

- candidate identity и mutation attribution;
- deterministic typed checks;
- approvals и verdicts;
- append-only evidence/replay;
- provenance manifest;
- export/verify/attestation.

Только для `run`:

- agent workflow graph;
- runtime adapters и model routing;
- loopbacks и adaptive depth;
- controller-owned delivery.

Такое разделение сохраняет уже написанный pipeline, но позволяет получить
первую ценность без обязательной замены пользовательского workflow.

---

## 5. Правила приоритизации Codex

Позиция попадает выше, если она:

1. делает центральное заявление продукта истиннее;
2. снижает цену первого запуска;
3. переиспользует существующее сильное ядро;
4. создаёт наблюдаемый артефакт или измеримый результат;
5. обратима и не создаёт большой support surface;
6. основана на проверенном факте, а не только на прогнозе рынка.

P0 означает «входит в следующий доказательный вертикальный срез», а не
«когда-нибудь критично». P1 начинается только после достижения exit criteria
P0. P2 требует пользовательского сигнала. Parking lot не является обещанием.

---

## 6. Приоритизированный backlog Codex

### P0 / Release V0 — переносимое доказательство и дешёвый вход

Цель релиза: человек, не имеющий исходного репозитория и полного pipeline,
может проверить, что именно было проверено и кем создано изменение.

| ID | Фича | Scope / definition of done | Зависит от |
|---|---|---|---|
| **V0-1** | **Test-mutation provenance + policy** | Контроллер классифицирует changed paths (`source/tests/meta/infra/generated/unknown`) по каждой стадии. Bundle и review показывают добавленные, изменённые и удалённые тесты. Изменение существующих тестов source-стадией по умолчанию требует exact-subject approval; policy configurable, результат fail-closed. | — |
| **V0-2** | **Provenance manifest v1** | В attempt/run identity входят digests prompt, agent definition, check definitions, controller binary, runtime executable/version, заявленные provider/model, base/candidate tree. Неизвестное значение записывается как `unknown`, а не угадывается. Resume обнаруживает drift authority-bearing полей. | — |
| **V0-3** | **Attestation predicate v1** | Версионированная ai-team predicate schema связывает candidate subject с run, spec/config/workflow, checks, mutations, approvals, verdicts и provenance digests. Есть canonical JSON, golden fixtures и compatibility policy. Формат совместим с in-toto Statement; подпись пока опциональна. | V0-1, V0-2 |
| **V0-4** | **`ai-team export` + `verify <bundle>`** | Детерминированный portable bundle содержит только schema-whitelisted evidence и manifests. `verify` работает без исходного repo/`.ai-team`, проверяет archive paths, sizes, digests, chain, schema и subject. Raw logs, stdout/stderr и произвольные artifact bodies исключены по умолчанию; остаются их digests и typed summaries. Tamper-тесты меняют каждый включённый класс данных и обязаны падать. | V0-3 |
| **V0-5** | **`ai-team gate` MVP** | Вход: trusted local base/candidate + spec + check suite. Выход: diff policy, typed checks, attestation bundle и стабильные exit codes. Spec fingerprinted и привязана к bundle, но без runtime не объявляется семантически проверенной. LLM-review не включён по умолчанию; код максимально переиспользует существующие packages, а не копирует pipeline. Режим `untrusted` отсутствует до containment. | V0-1, V0-4 |
| **V0-6** | **Generic JUnit XML typed adapter** | Строгий parser с bounded input, zero-test detection, suite/test counts, failures/errors/skips и raw-output digest. Golden fixtures минимум для pytest/Jest/Gradle/Maven exporters; заявляется совместимость только для реально проверенных fixtures. | — |
| **V0-7** | **Demo + CI action + truthful README** | Один demo-repo показывает `gate → bundle → verify`; один version-pinned CI snippet; branch protection требует этот check в самом ai-team. README явно разделяет `run`, `gate`, local integrity, signed authenticity и отсутствие OS sandbox. | V0-4, V0-5, V0-6 |

**Exit criteria V0:**

- `verify` на чистой машине проверяет bundle без исходного repository;
- одно изменение байта в любом manifest/evidence обнаруживается;
- вывод отвечает: base/candidate, какие тесты менялись и какой стадией, какие
  checks запускались, какой runtime/model был заявлен и кто что одобрил;
- demo подключается одним CI-файлом и проходит на Go и одном JUnit-producing
  стеке;
- минимум три внешних человека запускают demo; хотя бы один без помощи автора
  понимает, что bundle доказывает и чего он не доказывает.

### P1 / Release P1 — безопасная ширина полного pipeline

Начинать после V0 или после конкретного adopter request.

| ID | Фича | Scope / definition of done | Decision gate |
|---|---|---|---|
| **P1-1** | **RuntimeAdapter contract** | Разделить `launch`, `session protocol`, `capabilities`, `identity`, `usage` и `policy mapping`. OpenCode становится первой реализацией без изменения текущих guarantees. Unknown capability в strict profile блокирует запуск. | Contract не должен содержать vendor-specific поля |
| **P1-2** | **ACP feasibility spike** | За ограниченный спайк проверить Codex/Claude ACP implementations: headless lifecycle, cwd, file/terminal delegation, cancellation, permissions, model identity, usage, error taxonomy. Результат — conformance matrix и решение `ACP / native / hybrid`, не production adapter. | Если обязательные policy surfaces нельзя enforce — ACP не default |
| **P1-3** | **Второй verified runtime** | Реализовать ровно один второй runtime выбранным после P1-2 способом. Offline contract tests + optional live canary; support level основан на evidence. | Сначала реальный пользовательский runtime |
| **P1-4** | **Containment profile v1 + receipt** | Один поддерживаемый OS/backend, fs/net/proc/env threat model, process cleanup, deny-by-default network, platform-specific tests. Strict mode fail-closed. Необеспеченные оси честно отмечаются. | Обязателен до `gate --untrusted` |
| **P1-5** | **Signed bundle** | DSSE envelope для V0-3 с local/pluggable signer; verifier разделяет integrity и signer identity. Signing failure не маскируется unsigned success в strict policy. Публичный Sigstore/Rekor flow остаётся P2-6. | Сначала стабильная predicate v1 |
| **P1-6** | **Redaction + retention contract** | Классификация полей, secrets scanner для export, явные include/exclude policies, retention без обещания legal compliance. Raw logs opt-in. | Обязателен до SIEM/external archive |
| **P1-7** | **Budget guard и usage truthfulness** | Лимиты по wall time/attempts доступны всегда; tokens/cost только при attested adapter support. Нет оценки «known» по косвенным данным. Budget decision входит в evidence. | Не считать ACP гарантированным источником usage |
| **P1-8** | **CI-parity check import** | Импорт ограниченного, объяснимого набора checks из project config/CI без исполнения произвольного YAML. Сгенерированный effective suite показывается и fingerprinted до запуска. | Начать с одного формата по adopter request |

**Exit criteria P1:**

- один и тот же workflow проходит на OpenCode и втором runtime без ослабления
  mutation/policy/evidence contracts;
- strict containment действительно отказывает при отсутствии backend;
- signed bundle проверяется стандартным инструментом и ai-team verifier;
- usage явно различает attested, reported и unavailable;
- один внешний проект использует `gate` или `run` регулярно не менее двух недель.

### P2 — развивать только после наблюдаемого сигнала

| ID | Фича | Сигнал для старта |
|---|---|---|
| **P2-1** | **Adaptive workflow depth** | Есть baseline стоимости/времени по профилям и видно, что static `fast/standard/regulated` недостаточно |
| **P2-2** | **Risk-triggered multi-model review** | На реальных runs есть класс high-risk изменений, где второй независимый review ловит пропуски чаще своей стоимости |
| **P2-3** | **Parallel review DAG** | P2-2 полезен, но wall-clock review стал заметным bottleneck |
| **P2-4** | **OTel exporter / SIEM sink** | Есть оператор с OTel/SIEM и согласована privacy/redaction policy; использовать версионированные semconv, не считать GenAI conventions стабильными по умолчанию |
| **P2-5** | **Read-only MCP server** | Появился второй реальный consumer статуса/evidence кроме встроенного web |
| **P2-6** | **Sigstore/Rekor transparency anchoring** | Signed bundles используются и есть требование независимого timestamp/existence proof |
| **P2-7** | **AC → check → evidence trace** | Пользователь приносит формальные acceptance criteria и готов поддерживать identifiers |
| **P2-8** | **Forge adapters / multi-repo** | Есть конкретный GitLab/Gitea/multi-repo adopter и его workflow |
| **P2-9** | **Compliance evidence pack** | Есть regulated design partner, scope assessment и legal/security owner; название не обещает certification |
| **P2-10** | **Evidence reader in web** | Bundle остаётся непонятным через CLI и пользователи явно выбирают web как поверхность |

### Parking lot / не планировать сейчас

| Идея | Решение | Причина / условие возврата |
|---|---|---|
| Богатый per-harness descriptor zoo | **Не делать в текущем виде** | Нужен тонкий RuntimeAdapter + ACP/native implementations, а не параллельная спецификация транспорта |
| Отдельные Codex и Claude adapters одновременно | **Не делать** | Сначала один второй runtime и conformance evidence |
| Durable execution/time-travel | **Заморозить** | Текущие lifecycle resume + immutable evidence закрывают наблюдаемую потребность |
| 4-мерный verdict и новый completion vector | **Заморозить** | Существующие verdict/terminal states достаточны до конкретного routing failure |
| Plan-completeness S0/S1/S2 | **Заморозить** | Вернуться при появлении команды/regulated policy, а не одного автора |
| Small-model packets 4k/8k/16k | **Не делать в этой форме** | Оптимизация под незафиксированный target model; нужен реальный cost/quality experiment |
| Effective-config explanation для каждого поля | **Заморозить** | Вернуться при реальной диагностике precedence bugs |
| Context compaction checkpoints | **Заморозить** | Runtime responsibility, пока не доказан cross-runtime failure |
| Finding→root-cause→fix-impact defect system | **Выделить как отдельную будущую capability** | Не смешивать defect management с базовым portable evidence |
| Reviewer personas | **Заморозить** | Нет калиброванного корпуса и измеримой пользы |
| Dashboard time-travel | **Не делать** | Portable bundle/replay решают проверку дешевле |
| Встраиваемая библиотека | **Заморозить** | Только после запроса конкретного embedding consumer |
| Slack/email notifiers | **Заморозить** | Только при появлении команды-пользователя |
| Новые cloud scheduler/CAS features | **Заморозить** | Поддерживать существующее, не расширять до external adoption |
| Удаление `report/notifier/eval` | **Не делать пакетом** | Сначала telemetry/usage, затем отдельные deprecation decisions |
| Собственный внешний anchor format | **Не делать** | Использовать in-toto/DSSE и стандартные signing/transparency механизмы |
| Заявление «EU AI Act compliant» | **Запрещено до review** | Evidence — только один фрагмент compliance scope |

---

## 7. Порядок исполнения и зависимости

Рекомендуемая последовательность, если проект ведёт один человек:

1. **Integrity slice:** V0-1 → V0-2 → V0-3.
2. **Portability slice:** V0-4 и его tamper/compatibility tests.
3. **Adoption slice:** V0-5 → V0-6 → V0-7.
4. **Runtime decision:** P1-1 → P1-2 → решение ACP/native/hybrid → P1-3.
5. **Strict execution:** P1-4; только после него включать untrusted code mode.
6. **Authenticity/operations:** P1-5 → P1-6 → P1-7/P1-8.
7. **Scale features:** только позиции P2, для которых сработал указанный сигнал.

Не стоит параллельно открывать больше одного вертикального среза и одной
маленькой hygiene-задачи. Иначе backlog снова превратится в список начатых
подсистем.

---

## 8. Метрики и stop conditions

| Гипотеза | Метрика | Срок наблюдения после релиза | Stop / pivot |
|---|---|---|---|
| Portable evidence понятен | 3 внешних запуска demo; ≥1 человек правильно пересказывает guarantees и gaps | 4 недели | Улучшать schema/output/story, не добавлять новые evidence axes |
| `gate` снижает порог входа | ≥1 внешний repo запускает gate повторно в CI | 8 недель | Если нет — не строить SIEM/compliance/UI вокруг Track V |
| Второй runtime нужен | ≥1 пользователь блокируется на OpenCode | До P1-3 | Без запроса ограничиться ACP spike и текущим OpenCode |
| Adaptive depth экономит | После появления данных: ≥2× снижение median cost/time без роста escaped failures | 4 недели A/B | Если сигнала нет — оставить явные профили |
| Regulated niche реальна | ≥3 discovery-интервью, ≥1 design partner с owner требований | До P2-9 | Убрать compliance из позиционирования |
| Web является продуктовой поверхностью | Пользователи открывают evidence в web чаще CLI/bundle | До P2-10 | Оставить web operational/debug UI |
| Cloud-контур нужен | Есть multi-worker/self-hosted adopter | До новых cloud features | Только maintenance/security fixes |

---

## 9. Риски собственной стратегии

1. `gate` может оказаться полезной CI-фичей, но не самостоятельным продуктом.
   Это всё равно дешёвый тест: он переиспользует сильное ядро и улучшает `run`.
2. Portable bundle без доверенной подписи доказывает целостность только внутри
   переданного набора. Поэтому UI и docs обязаны разделять integrity,
   authenticity и external anchoring.
3. Классификация test paths неизбежно будет эвристической между языками.
   Unknown должен быть видимым классом, а policy — проектно расширяемой.
4. ACP может хорошо закрыть interactive transport, но плохо — headless
   deterministic orchestration. Спайк должен иметь заранее заданную fail
   matrix, иначе он станет бесконечной интеграцией.
5. V0 всё ещё довольно большой для одного автора. Если нужен ещё более узкий
   первый релиз, минимальный полезный cut — V0-1…V0-4 без `gate` и JUnit.
6. Заморозка cloud/UI может быть неправильной, если уже есть не отражённый в
   репозитории план монетизации или пользователь. Тогда соответствующие P2
   signals надо считать выполненными и переприоритизировать backlog.

---

## 10. Проверенные основания и границы уверенности

### Проверено в коде `origin/master`

- runtime interface есть, но CLI adapter принимает только OpenCode;
- isolation contract использует OpenCode-specific env/config;
- `coder` действительно имеет `allowed_paths: ['**']`;
- mutations уже сохраняются per attempt;
- candidate evidence уже содержит changed files, patch digest и check digests;
- delivery принимает только typed test evidence для exact workspace digest;
- typed adapter сейчас один: `go-test-json`;
- `verify` принимает локальный `run_id` и вызывает `VerifyAnchor(runDir)`;
- `anchor.json` хранится в том же run directory;
- artifactstore уже умеет content-addressed archive/restore, но это не
  пользовательский portable bundle contract;
- tokens остаются `unknown`, даже если usage envelope существует.

### Spot-check внешних decision-changing утверждений

- ACP — реальный JSON-RPC client↔agent protocol с capability negotiation,
  permission/filesystem/terminal surfaces и registry. Его v1 schema не
  содержит обязательных usage/cost полей.
- in-toto Statement связывает immutable subjects по digest с typed predicate;
  DSSE задаёт стандартный signed envelope.
- Sigstore/cosign умеет проверять in-toto attestations, но signing и
  transparency не возникают автоматически от выбора in-toto schema.
- EU AI Act содержит обязанности по логированию/хранению для определённого
  scope high-risk systems; это не универсальная обязанность любого coding
  agent workflow.
- OpenTelemetry GenAI conventions существуют, но часть поверхности имеет
  development/moved status; exporter должен версионировать mapping.

### Первичные ссылки

- [ACP protocol overview](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v2/overview.mdx)
- [ACP v1 schema](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v1/schema.json)
- [ACP registry](https://github.com/agentclientprotocol/registry)
- [in-toto Statement v1](https://github.com/in-toto/attestation/blob/main/spec/v1/statement.md)
- [DSSE protocol](https://github.com/secure-systems-lab/dsse/blob/master/protocol.md)
- [Sigstore: verifying in-toto attestations](https://docs.sigstore.dev/cosign/verifying/attestation/)
- [Regulation (EU) 2024/1689](https://eur-lex.europa.eu/eli/reg/2024/1689/oj)
- [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/)

Остальные рыночные числа и конкурентные оценки из `FEATURES.cloud.md` в этом
документе не считаются independently verified. Они полезны как гипотезы, но не
нужны для принятия ближайшего решения V0.

---

## 11. Финальная рекомендация

Не выбирать между «вариантом первой модели» и «вариантом Claude» целиком.

- Из `FEATURES.md` взять инженерную строгость и понимание control-plane
  contracts.
- Из `FEATURES.cloud.md` взять продуктовый фокус, portability, `gate`,
  test-tamper finding и дисциплину остановки.
- Отдельно исправить две крайности: не переизобретать стандарты, но и не
  передавать стандарту ответственность, которой в нём нет.

**Первый конкретный шаг:** спроектировать V0-1…V0-3 как одну совместимую
schema-вертикаль — test mutation facts + provenance manifest + attestation
predicate. После этого `export/verify bundle` становится механической
продуктовой поверхностью, а не ещё одним самодельным отчётом.
