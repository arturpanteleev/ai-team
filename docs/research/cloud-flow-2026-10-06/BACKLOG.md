# Единый бэклог: облачная AI-SDLC-система

Порядок выполнения: все **Major** по возрастанию ID, затем **Minor**, затем **незначительные**. На каждую задачу назначается отдельный sub-agent; его патч проверяется, затем открывается PR, PR проходит отдельное ревью. Исправления ревью выполняет новый агент. Статус меняется по фактическому этапу доставки.

**Статусы:** `Новая` → `В работе` → `На ревью` → `В PR` → `Готова к развёртыванию` → `Развёрнута`. `Блокирована` используется, когда нет обязательного процесса или внешнего решения.

**Текущий статус:** MAJ-01–MAJ-06 — готовы к развёртыванию; MAJ-07 — в работе. Влиты архитектурный gate (#193), worker-result v2 (#194), file-shaped порты approvals/evidence (#195–#196), SQLite approval persistence (#197), worker approval guard (#198), process-bound execution identity (#199), фильтрация окружения (#200), trust model (#201), run-scoped controller API для recorder/approval reads и requests (#202), per-request nonce/timestamp с bounded replay cache и expiry/replay проверками (#203) и конкурентные replay tests (#204). В текущем slice lifecycle create/load/save pipeline также маршрутизируются через controller-owned API и используются recovery dispatcher; локальный CLI сохраняет filesystem store. Это application-level ownership, не OS boundary: worker всё ещё работает под той же OS identity и видит target filesystem, поэтому способен напрямую обратиться к известному пути state/SQLite. Незавершёнными остаются separation state/API на уровне ОС, filesystem/network isolation, evidence/artifact separation, backup/restore и deployment smoke. deployment manifests и приёмка изолированного пилота заблокированы до OS-enforced разделения controller state и worker.

## Приоритет: Major

Источник и границы проверки: [исследование](README.md). Задачи выполняются последовательно, отдельным sub-agent на каждую.
Major означает блокер целевого пользовательского пути; не означает, что каждую задачу нужно делать до внутренней презентации.

### MAJ-01. Бизнес-намерение → уточнения → согласованное ТЗ

**Статус:** Готова к развёртыванию

**Тип:** недостающая возможность. **Почему:** расплывчатая цель сейчас остаётся строкой task; web не поддерживает вопросы аналитика, BLOCKED может потребовать нового run.

**Сделать:** создать durable brief/intention с историей уточнений и версиями; протокол вопросов и ответов агента; состояние ожидания ввода человека; UI для Product и заказчика. Связать согласованную версию brief с `proposal.md`/product spec и run. Перед архитектором предусмотреть явно выбранную политику согласования ТЗ. Не пытаться гарантировать увеличение прибыли одним фактом запуска AI.

**Где:** `pkg/runtime/opencode.go`, `pkg/pipeline/{execute,stage}.go`, `pkg/lifecycle/state.go`, `pkg/web/{server,commands}.go`, `web/src/pages`, `agents/analyst`.

**Готово, когда:** пользователь пишет «увеличить доход продаж», получает вопросы; другой участник отвечает после закрытия первого браузера; ответы сохраняются; аналитик формирует ТЗ с целью, ограничениями и проверяемыми AC; без ответа задача ждёт, не исчезает и не требует копировать всё в новый task. Есть тест такого сценария через browser + mock runtime.

**Зависимости:** для удобного облачного пути MAJ-03/04; проектирование вместе с MAJ-02.

### MAJ-02. Работа людей над артефактами и управляемый возврат

**Статус:** Готова к развёртыванию

**Тип:** недостающий командный сценарий. **Почему:** чтение Markdown и approval-кнопки не дают продакту исправить требования, архитектору обновить план, QA вернуть ошибку в требования.

**Сделать:** версии человеческих изменений и комментариев к артефактам, явные handoff/return действия по разрешённым графом маршрутам; причины возврата и передача feedback агенту. Добавить готовые пути architect → analyst, reviewer/tester → architect или coder. Разрешить оператору запросить остановку на безопасной границе этапа; не подменять immutable evidence редактированием файлов задним числом. При новой версии инвалидировать зависимые попытки и approval subjects.

**Где:** `pkg/approval`, `pkg/pipeline/{execute,approvals,stage}.go`, `pkg/config/load.go`, `pkg/web/server.go`, `web/src/pages/{PipelineDetail,ArtifactViewer}.tsx`.

**Готово, когда:** Product меняет ТЗ после замечания архитектора; видно автора и версии; coder получает новый план; прежнее ревью не разрешает delivery нового кандидата. Два одновременных изменения не затирают друг друга; запрещённый возврат объясняется. QA может вернуть ошибку требований нужной роли без создания несвязанного run.

**Зависимости:** MAJ-01, политика прав MAJ-05. Сам граф переписывать не нужно.

### MAJ-03. Починить полный путь web → очередь → видимый run

**Статус:** Готова к развёртыванию

**Тип:** воспроизведённые дефекты. **Почему:** scheduler preflight возвращает `checks:null`; React падает; даже после null-check `unknown` выключает запуск; принятые queued jobs отсутствуют в `/api/pipelines`.

**Сделать:** согласовать контракт readiness (`ready/blocked/unknown`) между server и UI; обработать отсутствующие checks; показывать честное ожидание worker, не требуя локального runtime для enqueue. Персистить и отображать принятую задачу сразу, со статусом queued и причиной последующей ошибки до старта run; поддержать отмену из очереди. Связать queue identity с run projection и обновлениями событий.

**Где:** `pkg/control/controller.go:112`, `pkg/preflight/preflight.go`, `pkg/scheduler/{engine,queue,poller}.go`, `pkg/web/store/sqlite.go`, `web/src/pages/Dashboard.tsx:99`.

**Готово, когда:** реальный Chromium открывает scheduler-режим без console errors; можно создать задачу без worker; она сразу видна как queued; после появления worker переходит в running; worker preflight failure виден пользователю; reload и отмена queued задачи работают. E2E должен загрузить React, а не только послать HTTP POST.

**Доказательство:** [queue-repro.json](evidence/queue-repro.json), [browser-checks.json](evidence/browser-checks.json).

### MAJ-04. Восстановление CSRF и команд после reload/reconnect

**Статус:** Готова к развёртыванию

**Тип:** HTTP-дефект подтверждён, браузерное следствие прослежено по коду. **Почему:** cookie-сессия существует, но новый JS-контекст не знает CSRF; повторный `openSession()` требует отсутствующий Bearer.

**Сделать:** получать CSRF для существующей валидной сессии через защищённый same-origin контракт; корректно обрабатывать истечение сессии, 401 и повторный вход. Не переносить signing secret или долговечный Bearer в небезопасное клиентское хранилище.

**Где:** `web/src/api.ts:21`, `web/src/App.tsx:32`, `pkg/web/commands.go`.

**Готово, когда:** login → reload → start/decision/resume работают; открытие второго окна с той же cookie работает; истечение сессии приводит к понятному повторному входу; CSRF-негативные проверки остаются зелёными. Проверить в браузере с auth, а не только в тесте функций API.

**Доказательство:** [auth-refresh-repro.json](evidence/auth-refresh-repro.json).

### MAJ-05. Управляемые пользователи и права команды

**Статус:** Готова к развёртыванию

**Тип:** недостающий продуктовый слой поверх существующих токенов/RBAC. **Почему:** выпуск токена администратором CLI не равен управлению участниками. Нет membership, удаления доступа и реестра пользователей; read endpoints доступны всем аутентифицированным участникам данного сервера.

**Сделать:** минимальную модель пользователей и членства одной команды, приглашение/активацию, назначение и отзыв ролей, отзыв сессий, аудит действий. Явно определить доступ к проекту и артефактам и права start/resume/cancel/decision/edit. Связать start/cancel и изменения артефактов с доверенным actor. При одном проекте не вводить сложную multi-tenant модель без необходимости.

**Где:** `pkg/cloudidentity/{identity,token}.go`, `pkg/web/{commands,server}.go`, `pkg/web/store/sqlite.go`, `web/src/pages/Login.tsx`.

**Готово, когда:** администратор приглашает Product и QA; участники имеют разные разрешения; удалённый участник не продолжает работу по старой сессии; события имеют проверенного автора; доступ нельзя расширить полями JSON. SSO может быть следующим отдельным этапом.

**Зависимости:** MAJ-04; нужно до пилота с управляемым общим доступом.

### MAJ-06. Реальное восстановление worker и маршрутизация заданий

**Статус:** Готова к развёртыванию (PR [#192](https://github.com/arturpanteleev/ai-team/pull/192))

**Тип:** подтверждённые ограничения контракта; полный crash-сценарий ещё нужно воспроизвести при реализации.

**Почему:** lease expiry повторяет исходный `start`; существующий run требует другой resume-путь. Claim не учитывает target исполнителя. Архив evidence не восстанавливает автоматически весь контекст выполнения на новом узле.

**Сделать:** reconciliation между queue/job/run lifecycle; idempotent admission/start и явную recovery-политику для уже созданного run; claim только подходящим worker; защиту от действий прежнего worker после потери lease. Для первой версии выбрать и описать один поддерживаемый вариант: общий постоянный volume с одинаковыми mount paths или полноценное восстановление из durable storage. Сохранить candidate, approvals, lifecycle, config и evidence; различать потерю браузера, web-процесса и worker.

**Где:** `pkg/scheduler/{queue,poller}.go`, `pkg/worker/{job,process}.go`, `pkg/pipeline/{engine,pipeline}.go`, `pkg/lifecycle`, `pkg/artifactstore`.

**Готово, когда:** kill worker в процессе стадии → другой worker продолжает тот же run без дублирующего commit/PR; kill после checkpoint и до complete job также безопасен; чужой target не забирается; старый lease не может публиковать результат; approvals и артефакты доступны после рестарта сервера. Нужны процессные интеграционные тесты с реальной потерей процесса.

**Связано:** [#116](https://github.com/arturpanteleev/ai-team/issues/116), [#117](https://github.com/arturpanteleev/ai-team/issues/117). **Доказательство:** [queue-contract.json](evidence/queue-contract.json). Не выдавать контрактный probe за уже проведённый chaos test.

### MAJ-07. Воспроизводимый облачный пилот с изоляцией исполнителей

**Статус:** В работе — PR [#193](https://github.com/arturpanteleev/ai-team/pull/193)–[#206](https://github.com/arturpanteleev/ai-team/pull/206) влиты. Web/scheduler launchers не передают worker-у `--db`; recorder, pending approval create/read, lifecycle create/load/save и durable business brief create/append/list/read идут через per-invocation scoped API. PR #203/#204 добавили nonce, expiry и конкурентные replay tests. Новый опциональный Linux slice запускает worker под bubblewrap: корень read-only, target read-write, настроенная SQLite DB с WAL/SHM/journal и `.ai-team/state/runs`, `.ai-team/state/approvals`, `.ai-team/state/candidates` маскируются; отдельный tmpfs на `/run` скрывает стандартные host service sockets, но pathname AF_UNIX sockets вне `/run` остаются потенциально доступными через bind `/`. Интеграционный probe в Linux CI проверяет DB/lifecycle/candidate-metadata sentinels и доступность workspace/worktree; отсутствие runtime/ошибка запуска не должны приводить к запуску без sandbox. Это ограниченная файловая граница, а не полный cloud isolation и не `ENFORCED` receipt: `.ai-team/runs` brief-файлы, evidence/manifests, candidate worktree contents, artifacts и прочие файлы target остаются доступны worker-у напрямую; только candidate metadata path скрывается в bubblewrap. Brief и candidate metadata API доказывают ownership штатного pipeline-пути, а не OS изоляцию содержимого. Изолированный network namespace блокирует прямой внешний IP egress. Пользователь выбрал **OpenAI** первым провайдером; текущий непубликуемый code slice добавляет controller-owned CONNECT proxy по per-invocation Unix socket, capability и namespace-local HTTP proxy, разрешая только `api.openai.com:443`, а OpenCode получает controller URL через `HTTPS_PROXY`/`HTTP_PROXY` и `NO_PROXY` для localhost. Fake TLS upstream probe должен доказать ограниченный туннель, deny прочих host/port и сохранение запрета прямого worker TCP; это не подтверждает реальный OpenAI model round-trip или совместимость каждой установленной версии OpenCode. Остаются независимая OS identity, typed artifact transfer, backup/restore и deployment smoke. Приёмка MAJ-07 и deployment manifests остаются заблокированы до закрытия этих пунктов и destructive runtime verification; приёмка изолированного пилота требует дальнейшего закрытия state/API, evidence/artifact и network gaps.

Следующий реализованный bounded шаг переносит candidate metadata create/read на run/target-scoped controller API. Pipeline использует API при create, resume и recovery, а local CLI остаётся на filesystem store. В Linux bubblewrap скрывается только `.ai-team/state/candidates`; новый CI probe должен подтвердить недоступность metadata sentinel и доступность sentinel в candidate worktree. Worktree, `.ai-team/runs`, artifacts и прочие файлы target остаются worker-visible. Этот шаг не доказывает полную изоляцию candidate/evidence/artifact и не закрывает MAJ-07.

Следующий узкий шаг добавляет controller-owned candidate-absence marker для bubblewrap cloud runs. До запуска worker ProcessEngine сам определяет Git eligibility; только start/recovery не-Git target может записать marker в `.ai-team/state/candidates`, который уже скрыт namespace-local tmpfs. Scoped API разрешает только marker read на resume/recovery; worker не может создать marker. Tampered `run.json` не участвует в решении, а missing/corrupt marker остаётся fail-closed. Без bubblewrap target директория доступна на запись worker-у: в этом режиме marker не создаётся/не принимается и non-Git resume остаётся fail-closed. Это не расширяет scope маски и не доказывает изоляцию candidate contents, evidence или artifacts. Локальный CLI сохраняет прежнюю совместимость по provenance из target-файла, который не считается trust boundary.

Узкий runtime-evidence шаг расширяет Linux bubblewrap child probe: внутри реально запущенного worker подтверждаются успешный typed scoped API-вызов, отказ `admin.*`, и отсутствие родительских sentinel-переменных `AI_TEAM_AUTH_SECRET`, `AI_TEAM_SIGNING_KEY`, `AI_TEAM_DB_PASSWORD`, `AI_TEAM_HOSTING_WRITE_TOKEN`. В том же probe сохранены проверки host/direct TCP, OpenAI-only CONNECT proxy, скрытых controller-state путей и доступа к target/worktree. `OPENAI_API_KEY` не считается control-plane secret и этим тестом не проверяется. Доказательство относится только к четырём переменным, одному API-вызову и данной Linux bubblewrap конфигурации; полный контроль admin endpoints, других источников credentials, deployment runtime, evidence/artifact и recovery остаётся открытым, MAJ-07 не закрыта.

Проверка следующего evidence-isolation шага подтвердила зависимость: worker child исполняет pipeline через `pipeline.RunEngine.Start` → `Pipeline.RunWithResult`; локальные вызовы также доступны через `Pipeline.Run`/`Pipeline.RunWithResult`, а resume снова входит в `RunWithResult`. Пути всё ещё зависят от filesystem state в `{target}/.ai-team/runs/<run_id>`. Resume вызывает `evidence.VerifyResumeEvidence`/`evidence.Resume`, читает attempt manifests; stage/finalize/delivery используют `RunDir`/`LogDir` и path-based операции. В отдельных bounded slices durable initial/clarification brief переведён на typed run-scoped controller API (persistent brief остаётся доступен через writable target), а candidate metadata create/read проходит через controller API и её каталог маскируется в bubblewrap; сам candidate worktree остаётся видимым worker-у. Открытыми остаются attempt manifests, artifacts, usage, containment, attestation, terminal delivery record/digest, human question answers и answer inputs, return-feedback inputs, cancellation markers, recovery и delivery verification/recovery. Нельзя накладывать tmpfs на весь каталог: остальные resume/evidence, отмена/recovery и публикация всё ещё path-based. Следующий slice должен перенести один из оставшихся путей на controller-owned typed store/API и сохранить его start/resume/recovery semantics; Linux probe для OS-недоступности уместен только для конкретного перенесённого каталога, сохраняя доступ к candidate workspace. До этого evidence isolation остаётся открытым; частичная path-маска не считается выполнением.

Следующий bounded slice переносит финальную `metrics.UsageEnvelope` bubblewrap cloud-run из worker-visible `.ai-team/runs/<run_id>/usage.json` в controller-stored `.ai-team/state/usage/<run_id>.json`; содержимое остаётся worker-supplied и untrusted. Перед spawn controller создаёт per-run reservation, который выбирает authority для конкретного run и заставляет CLI fail-closed при missing/corrupt envelope; reservation другого run не меняет local fallback. Worker передаёт typed envelope через write-only invocation/run/target-scoped Unix API; controller проверяет schema и run identity, публикует файл без замены в рамках одной invocation (идемпотентный точный повтор), а поздний Resume/Recover может атомарно обновить сводку после interrupted finalization. Linux bubblewrap маскирует каталог. Loopback API без bubblewrap не получает usage write authority и продолжает legacy target-file output, который не является controller-owned. Linux child probe проверяет недоступность sentinel-файла, успешную API-запись и наличие envelope после выхода worker-а. Эта граница защищает от sandbox child данной invocation, не от процесса/пользователя с прямой записью в target. Это закрывает только usage summary; evidence/artifact, containment, attestation, delivery и другие перечисленные пути остаются worker-visible, MAJ-07 не закрыта.

Следующий узкий bubblewrap шаг маскирует durable brief source только активного run: `.ai-team/runs/<run_id>/brief`. Поток данных уже проходит через `brief.list`/`brief.read` scoped controller API, а полученное содержимое materialize-ится отдельно в candidate workspace; controller store читает и пишет исходный host-каталог вне mount namespace. Linux child probe заранее создаёт immutable brief, подтверждает отказ прямого чтения, попытку записи только в namespace-local tmpfs, успешные API list/read и сохранность host-копии после child exit. Local и non-bubblewrap пути не меняются. Маска не включает остальные run evidence/manifests, artifacts или другие каталоги; это одна граница, MAJ-07 остаётся открытой.

Введён package-private `eventLog` seam для Store append и resume replay; файловый backend остаётся единственной поддерживаемой реализацией, поскольку pipeline verification/recovery, Cancel, attestation и другие path-based readers пока требуют локальный `events.jsonl`. Unix append сериализуется flock-ом на том же файле; прочие платформы получают in-process per-path mutex. JSONL bytes, hash-chain, sync и terminal anchor сохранены. Публичного API для независимого backend нет: этот seam — только внутренний фундамент. Bubblewrap mount/API не менялись, runtime probe не добавлен, журнал остаётся видимым и доступным worker-у. Это не controller ownership и не закрытие MAJ-07.

Промежуточный slice в PR [#217](https://github.com/arturpanteleev/ai-team/pull/217) переносит terminal delivery record bubblewrap worker-а в `.ai-team/state/delivery/<runID>.json` через typed run-scoped controller API; recovery и `FindDelivered` читают новое хранилище с fallback на legacy `runDir/delivery.json`. Проверены валидация RunID, идемпотентный точный retry, запрет конфликтующей перезаписи и bubblewrap probe прямого доступа/API-записи. Записи остаются worker-supplied и недоверенными. Это промежуточный технический шаг; MAJ-07 остаётся открытой.

**Тип:** недостающий deployment/operations слой. **Почему:** disposable subprocess не создаёт границу изоляции; strict containment сейчас отклоняет запуск; установку persistent volumes/TLS/secrets оператор собирает самостоятельно.

**Сделать:** одну поддерживаемую reference deployment-конфигурацию для сервера/облака: отдельный control plane и ограниченные worker-контейнеры, credentials только по назначению, persistent volumes, TLS ingress, restart policy, backup/restore. Runtime не должен иметь возможность сам записать разрешение человека или вызвать административный control API. Проверять эффективные ограничения, не заявлять strict на основании наличия Dockerfile.

**Где:** deployment files (новые), `pkg/worker/process.go`, `pkg/runtime`, `pkg/pipeline/pipeline.go:208`, `pkg/containment`, security/cloud guides.

**Готово, когда:** по инструкции запускается сервер для одной команды; браузеры двух пользователей подключаются; worker не читает секреты control plane и не изменяет approvals/evidence напрямую; контейнер удаляется без потери требуемого состояния; проверено восстановление из backup. Указать поддерживаемую инфраструктуру и ограничения.

**Связано:** [#152](https://github.com/arturpanteleev/ai-team/issues/152), [#153](https://github.com/arturpanteleev/ai-team/issues/153), [#154](https://github.com/arturpanteleev/ai-team/issues/154), [#155](https://github.com/arturpanteleev/ai-team/issues/155), [#174](https://github.com/arturpanteleev/ai-team/issues/174). Это связанные открытые вопросы, а не повторно подтверждённые здесь exploits.

**Зависимости:** согласовать storage/recovery с MAJ-06 и identity с MAJ-05.

### MAJ-08. Довести конец процесса до deploy и восстановления после его сбоя

**Статус:** Новая

**Тип:** недостающая возможность плюс разрыв интерфейса существующего delivery.

**Сделать:** отделить статусы проверки кода, Git delivery и deployment. Сначала показать фактический delivery outcome/PR и дать web-команду безопасного retry уже одобренного плана. Затем подключить один выбранный CI/CD-провайдер: ожидаемый commit/artifact, environment, запуск/ожидание, health/smoke result, URL приложения, retry и rollback либо явная операторская эскалация. Не считать общий `completed` доказательством deployment success.

**Где:** `pkg/delivery`, `pkg/pipeline/delivery_deferred.go`, `cmd/ai-team/deliver.go`, `pkg/web/{server,commands}.go`, `web/src/pages/PipelineDetail.tsx`; новый deployment adapter.

**Готово, когда:** после approval известный commit развёрнут в тестовой среде и виден URL; неуспешный push или deploy имеет отдельный статус; пользователь с телефона повторяет разрешённую операцию без SSH; изменившийся plan требует нового approval; повтор не создаёт дублирующий PR/deploy. Сбой проверки приложения не теряется за зелёным кодовым run.

**Связано:** [#121](https://github.com/arturpanteleev/ai-team/issues/121). Выбор провайдера нужен при реализации; сейчас произвольно не зафиксирован.

### MAJ-09. Полный основной сценарий с телефона

**Статус:** Новая

**Тип:** воспроизведённый UX-блокер явного требования пользователя.

**Сделать:** адаптивную навигацию, перенос/сворачивание фильтров, вертикальную форму, читаемые approvals/артефакты и действия, доступные пальцем. Проверить фон/возврат вкладки и восстановление подключения. Не ограничиваться уменьшением шрифта.

**Где:** `web/src/components/Layout.module.css`, `web/src/pages/{Dashboard,PipelineDetail,ArtifactViewer}.module.css`, соответствующие TSX.

**Готово, когда:** при 360/390/430 px все основные controls доступны без горизонтального поиска; можно создать задачу, прочитать ТЗ, принять решение, вернуться после reload и увидеть обновлённый статус. Проверить desktop regression и хотя бы один настоящий мобильный браузер перед обещанием phone-ready.

**Доказательство:** [browser-checks.json](evidence/browser-checks.json). **Зависимости:** MAJ-03/04 для полноценной проверки, MAJ-01/02 для дальнейшего мобильного discovery.

## Приоритет: Minor

Источник: [исследование](README.md). Все задачи предложены, не реализованы. Не требуются fan-out/join и SaaS для нескольких организаций: пользователь их не задавал.

### MIN-01. Управление шаблонами процесса и наглядный Flow

**Статус:** Новая

**Проблема:** граф конфигурируется YAML и agent overrides, а UI показывает узлы и список рёбер. Нет редактора/каталога шаблонов. Это ограничение удобства, а не отсутствие графового движка.

**Сделать:** выбор шаблона при запуске и предварительный просмотр маршрута с ролями, артефактами и возвратами; валидируемое редактирование и публикацию версии. В первой версии допустим YAML-редактор с диагностикой вместо drag-and-drop. Отделять конфигурацию нового run от уже закреплённой версии текущего; валидировать доступные cloud-роли.

**Где:** `pkg/config`, `pkg/workflow/graph.go`, `pkg/agent/registry.go`, `pkg/web/server.go`, `web/src/pages/PipelineDetail.tsx`.

**Приёмка:** новый шаблон с architect → analyst создаётся и проверяется; недостижимые узлы и безлимитные циклы отклоняются; запущенный run сохраняет свою версию; в Flow видны текущая стадия и история возвратов.

**Связано:** [#123](https://github.com/arturpanteleev/ai-team/issues/123). Расширение до DAG из [#126](https://github.com/arturpanteleev/ai-team/issues/126) и произвольных data-driven outcomes из [#122](https://github.com/arturpanteleev/ai-team/issues/122) не включено автоматически. Если бизнес сам должен настраивать процессы без технического участника, MIN-01 становится Major.

### MIN-02. Понятные решения и продолжение процесса

**Статус:** Новая

**Проблема:** deferred approvals показывают заведомо неработающие кнопки; после решения нужно отдельно нажимать Resume; Resume/Cancel доступны независимо от ситуации; payload приходится читать как canonical JSON; комментарий API не выведен в форму.

**Сделать:** показывать доступные действия по статусу и роли, пояснять deferred-политику, добавить комментарий и читаемое представление предмета решения со ссылками на артефакты. Предложить явное «Подтвердить и продолжить» с корректной обработкой частичного успеха. Технический JSON оставить раскрываемым.

**Где:** `web/src/pages/PipelineDetail.tsx`, `web/src/api.ts`, `pkg/web/commands.go`, `pkg/approval`.

**Приёмка:** нельзя нажать заведомо запрещённое действие; видно, что именно подтверждается и почему нужна эта роль; после сетевого сбоя ясно, сохранилось ли решение; повтор не дублирует действие. Backend остаётся источником разрешений.

**Зависимости:** MAJ-04, согласовать с MAJ-02/05.

### MIN-03. Наблюдаемость и адресная очередь задач для людей

**Статус:** Новая

**Проблема:** серверные queue/worker-состояния и ожидания людей не собраны в операционную картину. Для команды недостаточно смотреть только список run. `pkg/notifier` существует, поэтому не нужно создавать уведомления с нуля.

**Сделать:** «ждёт моего решения», кому и почему передан этап, время ожидания; адаптировать имеющиеся уведомления к handoff. Добавить health/readiness и видимость worker heartbeat, длины очереди, инфраструктурных ошибок; экспорт минимальных operational metrics. Не показывать неизвестный token/cost usage как точный ноль.

**Где:** `pkg/notifier`, `pkg/metrics`, `pkg/scheduler`, `pkg/web`, dashboard.

**Приёмка:** Product и QA видят свои ожидания; offline worker и очередь без исполнителей заметны; health различает живой HTTP и готовность исполнения; уведомления не дублируются при reconnect.

**Связано:** [#113](https://github.com/arturpanteleev/ai-team/issues/113), для достоверности стоимости [#170](https://github.com/arturpanteleev/ai-team/issues/170). **Зависимости:** MAJ-03/05/06.

### MIN-04. Документация и воспроизводимое демо именно целевого продукта

**Статус:** Новая

**Проблема:** «всё для одной команды» опережает проверенный browser/cloud путь; текущие примеры центрируются на доставке кода в Git, а замысел включает business discovery и deployment.

**Сделать:** разделить «работает сейчас», «требует оператора/внешней инфраструктуры», «планируется». Добавить один сценарий команды с Product, Architect, QA и Release Manager, настройкой возвратов и объяснением standard/regulated approvals. После реализации Major расширить сценарий до deploy. До этого маркировать его границу PR. Добавить браузерную регрессию демо-пути.

**Где:** `README.md`, `docs/index.md`, `docs/start/fit.md`, `docs/guides/dashboard.md`, tutorials/demo.

**Приёмка:** новый пользователь воспроизводит описанный сценарий на чистой reference installation; ни один шаг не требует недокументированной CLI-операции; будущие возможности не выдаются за текущие. Не копировать старые findings без проверки их актуальности.

## Приоритет: Незначительные

Источник: [исследование](README.md). Все задачи предложены, не реализованы; делать после блокеров.

### TRIV-01. Название и язык страницы

**Статус:** Новая

**Факт:** `web/index.html` содержит `<title>web</title>` и `lang="en"`, интерфейс преимущественно русский.

**Сделать:** название ai-team и корректный язык; при наличии run — осмысленный title карточки.

**Приёмка:** вкладка и screen reader правильно представляют продукт и язык. Не требуется полная система локализации.

### TRIV-02. Единый пользовательский словарь

**Статус:** Новая

**Факт:** на одном экране смешаны Pipeline Runs, Started, Duration, Approvals, Resume/Cancel и русские подписи.

**Сделать:** единые подписи «Задачи/Прогоны», «Продолжить», «Отменить», «Ждёт решения»; glossary для различия агентной роли и роли человека. Согласовать выбранное слово для run. Machine statuses/API values сохранять стабильными.

**Где:** `web/src/pages`, `web/src/components`, glossary docs.

**Приёмка:** бизнес-пользователь понимает действия без знания английских внутренних статусов; API совместим.

### TRIV-03. Краткое отображение технических идентификаторов

**Статус:** Новая

**Факт:** run ID, subject/candidate SHA и canonical JSON занимают значительную часть экрана решения.

**Сделать:** краткие хеши с копированием полного значения, понятные подписи, технические подробности в раскрываемом блоке. Существенные данные плана и точная identity подтверждения должны оставаться доступными.

**Где:** `web/src/pages/PipelineDetail.tsx`, компоненты stage/artifact.

**Приёмка:** карточка читается на desktop и телефоне; полный hash копируется без потерь; отображение не изменяет подписываемый/подтверждаемый subject. Согласовать с MIN-02 и MAJ-09.
