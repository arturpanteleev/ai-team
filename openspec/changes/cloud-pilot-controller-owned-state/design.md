# MAJ-07 design gate

## Текущая граница

Публичный control plane `web` в scheduler режиме пишет задания в SQLite.
`scheduler-worker` читает эту очередь и передаёт задание `worker.ProcessEngine`.
`ai-team worker` затем создаёт обычный `pipeline.RunEngine` и запускает pipeline
изнутри worker process. Раньше в web и scheduler режимах approvals записывались
в SQLite `web.db`, который был доступен worker напрямую. Теперь штатные web и
scheduler launchers пересылают worker recorder/approval операции через
run-scoped controller API без `--db`; общая OS identity и доступ к target
filesystem всё ещё позволяют worker попробовать открыть известный путь к БД.
В текущем slice lifecycle `Create`/`Load`/`Save` обычного pipeline и recovery
dispatcher проходят через run/target-scoped controller API; реальный
`lifecycle.Store` создаётся и вызывается в controller-процессе. Локальный CLI
по-прежнему использует filesystem store. `.ai-team/runs`, evidence и candidate
metadata продолжают писаться в целевой workspace из worker-процесса. Общая OS
identity и доступ к target filesystem позволяют worker-у обойти API и напрямую
изменить lifecycle-файл или открыть известный путь к SQLite. Это application-
level ownership, а не граница безопасности.

Дизайн контейнерного volume/network deployment без изменения этого контракта
будет ложным свидетельством изоляции. `strict` сейчас корректно отказывает
fail-closed и не должен обходиться deployment flag-ом.

PR #197 добавил SQLite-backed approval persistence. Worker application adapter
отклоняет `Decide` и `ResolveDeferred`; authenticated controller route сохраняет
полный store. Новый scoped controller API убирает `--db` из штатного worker launch и
пересылает recorder/approval операции в controller. Это снижает доступность
прямой записи через приложение, но не является границей изоляции: worker всё
ещё имеет ту же OS identity и доступ к target filesystem, откуда может
попытаться открыть SQLite по известному пути. Effective OS isolation отсутствует;
deployment manifests и пилотная приёмка остаются заблокированы до выделения
controller-only filesystem/credentials и разделения writable state.

После PR #199 `ProcessEngine` добавляет свежий `execution_id` каждому запуску и
проверяет его в результате. Disposable worker получает только документированный
runtime baseline и переменные из `AI_TEAM_WORKER_ENV_ALLOW`; его `HOME`, `TMPDIR`
и XDG-каталоги принадлежат временному каталогу задания. Это предотвращает
обычное наследование control-plane секретов из переменных окружения, но не
ограничивает доступ того же OS-пользователя к файлам и не скрывает target и его
данные от worker процесса.

В текущем implementation slice web и scheduler launcher больше не передают
`--db` worker-процессу и не открывают в нём recorder/approval SQLite stores.
Вместо этого controller поднимает на время одного invocation API с одноразовым
случайным bearer token. В Linux bubblewrap режиме API слушает Unix-domain socket
в private `TMPDIR`; worker запускается в отдельном network namespace без host
loopback и TCP egress. В остальных режимах API сохраняет loopback transport.
Каждый запрос несёт `run_id`, `operation`
и свежий `execution_id`; сервер принимает только точную scope-связку этого
запуска. API позволяет сообщать pipeline recorder events и создавать/читать
approvals только текущего run. Endpoint для decision/resolution и admin
операций отсутствует, а неизвестные методы отвергаются. Негативные тесты
проверяют чужой run, forged decision и admin method. Каждый
аутентифицированный запрос несёт случайный 256-битный nonce и `issued_at`:
сервер допускает
окно 30 секунд с 5 секундами допустимого опережения, отклоняет повтор nonce и
хранит не более 4096 активных nonce на invocation. При заполнении таблицы API
отказывает до освобождения истёкших записей. Тесты проверяют, что повторный,
просроченный, будущий или неполный запрос не доходит до dispatch и не дублирует
approval/recorder state. Это защита от случайного или повторного вызова
протокола, не защита от скомпрометированного процесса с доступом к токену:
worker может подписать новый nonce, пока жив invocation.

В следующем application-level slice pipeline получает узкий lifecycle store
port. В штатном web/scheduler worker он реализован typed вызовами
`lifecycle.create`, `lifecycle.load` и `lifecycle.save`; controller проверяет
run и target, а dispatch сериализован вместе с recorder и approval вызовами.
Load сверяет идентичности полученной записи; create/save не принимают чужой run
или target. Recovery dispatcher использует тот же injected port. CLI без
worker API по-прежнему создаёт локальный filesystem store. Перенос вызовов не
закрывает прямой filesystem доступ: lifecycle файлы остаются доступны общей OS
identity, а evidence и candidate файлы по-прежнему находятся в target.

Эта граница ограничивает штатный worker protocol, но НЕ является OS или
filesystem isolation: дочерний процесс работает под тем же OS identity и всё
ещё имеет доступ к target filesystem, включая возможность попытаться открыть
известный путь к БД напрямую. API listener доступен локально этому процессу,
а token передаётся ему через environment. Поэтому MAJ-07 остаётся незавершённой;
нужны отдельная identity/filesystem/network policy, writable-state separation,
tamper tests и восстановление после потери worker/controller.

## Минимальная целевая архитектура

### Controller

Controller остаётся доверенным и единственным writer для очереди, состояния
run, approval decisions, evidence chain/manifests, control API и delivery
credentials. Он отправляет job description с проверяемым job identity и
отдельной capability, выданной на короткий срок для одного результата.

### Worker

Worker получает только код/контекст конкретного candidate и runtime credential,
который нужен выбранному provider. Нет control DB или state mounts, host
container socket, browser auth secret, Git hosting write token либо ingress
route к административным endpoints. Worker container удаляется после задания;
его scratch не является источником истины.

### Job result protocol

### Trust assumptions и граница утверждений

Worker — недоверенный исполнитель, а его stdout, exit code, check summary,
артефакты, логи и заявленные digests — недоверенные входные данные. Мы не
предполагаем, что worker честен, что модель выполнила инструкцию или что команда
проверки действительно запускалась. Принятый результат доказывает только, что
controller получил ограниченный ответ от invocation с указанными job/run/action
и `execution_id`; он не доказывает корректность кода, успешное прохождение тестов,
происхождение файла от конкретного инструмента или отсутствие изменений worker-а.

Controller может самостоятельно проверить границы протокола: версию и строгую
схему, job/run/action/execution identity, активность job и lease, capability,
срок и одноразовость, nonce, лимиты байтов/количества, допустимое имя артефакта,
фактический digest полученных байтов и допустимость перехода. Digest связывает
записанные байты с manifest, но не подтверждает их смысл и не делает заявленный
worker-ом digest доверенным. Проверка содержимого считается успешной только если
её повторно выполнил controller либо отдельный доверенный verifier в изолированной
среде, которая получила те же зафиксированные байты и записала собственные
identity/version/result. Если результат проверки вычислил сам worker, controller
может сохранить это как worker claim, но MUST NOT трактовать как подтверждённый
успех проверки.

Артефакты принимаются как недоверенные candidate bytes: только через ограниченный
transfer в controller-owned staging, с лимитом размера и числа, allowlist
логических имён/типов и вычислением digest по реально принятым байтам. Запись в
authoritative evidence/run state делается controller-ом после валидации; worker
не передаёт произвольный абсолютный/относительный filesystem path, destination,
команду для controller-а или готовую запись evidence. Сначала staging, затем
валидация и commit; невалидный или частичный transfer не меняет authoritative
state. Повтор того же принятого результата идемпотентен; повтор capability или
результат другого invocation отклоняется. В durable evidence controller
различает `worker_reported` и `controller_verified` и хранит, кем/чем выполнена
каждая принятая проверка.

Human decision не является типом/полем worker result и не может быть получен из
`outcome`, artifact, check claim или capability. Решение создаётся только
аутентифицированным пользователем через controller session с RBAC и CSRF
защитой, сохраняется controller-ом с actor, timestamp и audit event. Worker
capability даёт право только передать результат своей job; у worker нет API
credential или маршрута для создания/изменения/разрешения human decision.

Текущая реализация уже ограничивает stdout result schema v3 до 4 KiB, принимает
только одно result line и сверяет run, operation и свежий `execution_id`; строгий
JSON отвергает неизвестные поля, включая поддельное поле решения. Это узкий
status protocol, не передача артефактов, capability, nonce или проверок: worker
сейчас имеет общий DB/workspace access, поэтому перечисленные controller-side
проверки и commit semantics являются требованиями будущего API refactor, а не
свойствами текущей системы.

До реализации protocol refactor нельзя объявлять worker-produced checks
подтверждёнными. Не переносить в API произвольный filesystem path или команду,
выбранную worker-ом; определить typed transfer, controller-side validation и
atomic commit по этим правилам.

## Проверяемые негативные сценарии

Протокол считается приемлемым только если тесты реально запускают скомпрометированный
worker harness и доказывают:

1. Удалённый/изменённый или произвольный approval не принимается, а действующее
   человеческое решение остаётся тем же.
2. Изменение/удаление существующего event log, manifests, run artifact и
   lifecycle с worker identity получает EACCES/denied; последующий verify
   сохраняет успех для исходного controller-owned run.
3. Admin API route недостижим из worker network namespace; прямой HTTP запрос
   с попыткой изменить роли/решение не проходит. Provider egress открывается
   только утверждённой сетевой политикой.
4. Capability с чужим job/run/action, просроченным временем, повторным nonce,
   отсутствующей/подменённой подписью, oversized payload или path traversal
   отклоняется и не изменяет controller state.
5. Kill во время стадии, потеря worker и рестарт контроллера не создают
   повторный side effect и не теряют pending human approval. Restore из backup
   восстанавливает валидируемую очередь, решение и evidence.

Smoke test deployment дополняет тесты протокола: ingress только HTTPS;
worker без control-plane secrets; non-root, read-only rootfs, dropped
capabilities, pid/memory/cpu limits, tmpfs scratch; worker с отдельной
service identity; restart policy для controller/queue poller; документированный
backup/restore. Compose/Kubernetes policy проверяется на работающей среде,
поскольку статический manifest не доказывает, что runtime её применил.

## Последовательность

### Bounded Linux bubblewrap filesystem slice

Web/scheduler launchers can opt in with `AI_TEAM_WORKER_SANDBOX=bubblewrap`.
On Linux, `ProcessEngine` wraps the child in bubblewrap user/PID/IPC/UTS/network
and mount namespaces, exposes the host root read-only, rebinds the configured
target read-write, and masks the configured SQLite database plus sidecars and
the lifecycle/legacy-approval state directories. A mode-0600 per-invocation
Unix socket under the worker's private `TMPDIR` carries the existing scoped
controller API, so the worker does not share the host network namespace. The
launcher overlays `/run` with a fresh tmpfs to hide standard host pathname
sockets there, such as Docker and system D-Bus sockets. Nonstandard host Unix
sockets outside `/run` remain potentially reachable through the read-only root
bind, subject to host permissions. Before creating later mounts, the launcher
resolves writable target, worker HOME/TMPDIR, and each agent-registry source /
destination and rejects any canonical path that overlaps `/run` in either
direction: equality, descendants, and ancestors such as `/`. This prevents
those mounts from reopening host `/run` over the tmpfs.
Before process creation, the launcher resolves and cleans the target and rejects
it if it resolves to `/`; otherwise the writable target bind would override the
read-only host-root bind for the entire filesystem.
If bubblewrap is absent or its namespace setup fails, the process fails without
an unsandboxed retry. This option is not enabled by default and does not change
the containment receipt. The Linux host must permit bubblewrap's unprivileged
user namespaces. On Ubuntu 24.04 and newer, AppArmor may deny them by default;
operators need a narrow AppArmor rule for the bubblewrap executable. The
reference CI profile is in `.github/ci-bwrap.apparmor`; it grants `userns` to
`/usr/bin/bwrap` without disabling the host-wide restriction. Runtime
availability is distinct from package presence, so a host-policy denial still
fails the worker invocation closed.

Before launch, the configured canonical DB path and each existing canonical
SQLite sidecar are checked for regular-file type and `st_nlink == 1`. The
launcher also checks files under the private lifecycle and legacy-approval
directories for regular-file type and a single link. It fails closed on
hard-link aliases because masking one pathname or directory would leave the
same file reachable elsewhere. Integration coverage places real sentinel files
at the DB, WAL, SHM, and rollback-journal canonical paths before spawning the
sandbox and checks that none of their secret bytes can be read inside it. Only
the explicitly listed DB paths and state directories are covered by this
slice.

The slice leaves `.ai-team/runs` evidence/manifests, candidate and artifact
paths, and host files outside the configured database potentially reachable
through the read-only host-root bind, subject to host permissions. The evidence
directory cannot safely be overlaid with tmpfs as another path-only masking
step: the child executes the pipeline and the current filesystem evidence
factory stores authoritative run data at `{target}/.ai-team/runs/<run_id>`.
`pipeline.RunEngine.Start` enters `Pipeline.RunWithResult` (and local callers may
use `Pipeline.Run`/`RunWithResult` directly); resume also reaches
`RunWithResult`. These flows write initial/versioned business briefs, read and
replay evidence, and append events and attempt manifests. Masking that directory
would break resume and hide new evidence from the controller after the worker
exits. A follow-up evidence boundary must replace the filesystem-shaped store
contract with a controller-owned typed evidence API/store. The current direct
file-dependency inventory is intentionally non-exhaustive and includes
`RunDir`/`LogDir`, attempt manifests and runtime logs, durable question answers
and answer inputs, immutable human return-feedback inputs, cancellation request
markers and recovery, candidate/artifact publication, usage, containment,
attestation, prepared-workspace and digest verification, and terminal delivery
records/recovery. The implementation must trace and migrate all current file
reads and writes, including brief/version, human feedback/answer, cancellation,
recovery, and delivery-verification flows; this list is not an exhaustive
acceptance boundary. Only after those operations no longer depend on paths in
the writable target can a Linux probe assert that controller evidence is
unreadable while candidate workspace access remains available. The worker's
`HOME` points to a fresh per-invocation temporary directory, so the original
host home is not exposed through `HOME`;
its original absolute path may still be reachable through the read-only `/`
bind, subject to host permissions. The read-only root bind does not provide
general host-secret isolation.

The durable business brief is now a bounded API-owned slice of this work. The
pipeline's brief store contract accepts a run ID and typed brief data only;
worker create/append/list/read operations are dispatched through the scoped
controller API. The controller persists immutable versions and returns the
selected version content. Initial creation is bound to the immutable job task
on start and the persisted lifecycle task on resume/recovery, so a worker
cannot create a different first intention. The child materializes that content
under per-invocation temporary staging at the writable target path
`{target}/.ai-team/artifacts`; it is visible to the worker process and is not a
security boundary. Cleanup is a
deferred `RemoveAll`, so abrupt worker termination may leave `.brief-*` staging
directories behind. This routes normal pipeline access through controller
ownership, but the persistent brief remains under the writable target mount and
is still directly readable/modifiable by a compromised worker. It does not
justify a Linux filesystem-isolation claim or a brief sentinel probe. Other
target evidence and artifacts remain outside this slice.

Candidate worktree identity now has a separate bounded ownership slice. The
pipeline uses a run/target-scoped typed controller API for candidate metadata
create and read; local CLI continues using the existing filesystem store.
The API validates schema, run ID, canonical target, and exact
`{target}/.ai-team/worktrees/{run_id}` identity, and rejects conflicting
metadata. Symlinked parent components in the target spelling resolve to the
same canonical target; the candidate manager rejects a symlink at the final
target component. Bubblewrap overlays only `{target}/.ai-team/state/candidates` with a
namespace-local tmpfs. The Linux probe reads a controller metadata sentinel and
a separate worktree sentinel to verify that metadata is hidden while worktree
access remains. This does not isolate candidate contents, artifacts, or the
remaining target evidence; it is not full candidate isolation or MAJ-07
acceptance. Resume fails closed when controller candidate metadata is missing;
worker-writable `run.json` provenance cannot establish that a candidate was
absent. Consequently, non-Git runs without a candidate record cannot currently
resume in controller-backed mode. Supporting them requires a separate
controller-owned absence marker. Local CLI retains legacy compatibility based
on target-file provenance, which is not a trust boundary.
The isolated network namespace has no direct external IP egress. A separate,
run-scoped TCP proxy now permits only `api.openai.com:443` for the selected
OpenAI provider; other hosts, ports, and providers remain blocked. The proxy
does not inspect HTTPS requests or responses, and the worker still receives
its provider credential, so this is destination restriction rather than
credential isolation or application-level mediation. Linux CI exercises the
proxy with a constrained fake TLS upstream; it does not perform a real OpenAI
model round-trip or establish compatibility with every OpenCode installation.
The target is still writable, and independent OS identity, artifact transfer,
recovery, backups, and deployment smoke remain open. Linux CI installs
bubblewrap and runs child-process probes for the scoped controller API,
loopback/direct-egress blocking, controller-state sentinels, and target
read/write. Passing these probes is evidence for only the specific properties
they exercise, not full worker isolation.

1. Зафиксировать trust assumptions и модель attestation результата. **Зафиксировано
   здесь:** worker result/checks/artifacts недоверен; controller подтверждает
   только correlation и собственные независимые проверки.
2. Спроектировать и протестировать typed worker result / controller commit
   протокол без пересылки человеческих решений как обычных worker outputs.
3. Уже добавлены SQLite approval store, общий persistence port, application-
   level worker adapter для approvals и controller API для recorder, approvals
   и lifecycle checkpoint calls. Worker launch не получает `--db`; pipeline
   использует controller-owned lifecycle port, а локальный CLI — filesystem
   store. Это не закрывает worker-у filesystem: отдельно нужны OS-enforced
   separation, отделение evidence/artifacts от worker scratch и восстановление
   существующих runs.
4. После позитивных и негативных протокольных тестов выбрать одну инфраструктуру
   и добавить воспроизводимый reference deployment, TLS, least-purpose secrets,
   persistent storage и backup/restore.
5. Запустить destructive recovery/network/filesystem probes на рабочей
   инфраструктуре; только тогда разрешить `strict` и снять блокер MAJ-07.

Оценка: это не deployment-only изменение. До начала протокольного refactor
нужна отдельная проверка trust model и schema; безопасное объявление текущей
cloud reference конфигурации завершённой невозможно.
