# MAJ-07 design gate

## Текущая граница

Публичный control plane `web` в scheduler режиме пишет задания в SQLite.
`scheduler-worker` читает эту очередь и передаёт задание `worker.ProcessEngine`.
`ai-team worker` затем создаёт обычный `pipeline.RunEngine` и запускает pipeline
изнутри worker process. В web и scheduler режимах approvals записываются в
SQLite `web.db`, который также открыт у controller для чтения и принятия
решений через authenticated API. Worker имеет прямой доступ к той же writable
БД и потому может подделать или удалить approval и сохранённый human decision.
Lifecycle, `.ai-team/runs`, evidence и candidate metadata по-прежнему пишутся
в целевой workspace. Approval storage теперь controller-readable и durable,
но такая общая база не является границей безопасности и не обеспечивает
controller-only writes.

Дизайн контейнерного volume/network deployment без изменения этого контракта
будет ложным свидетельством изоляции. `strict` сейчас корректно отказывает
fail-closed и не должен обходиться deployment flag-ом.

PR #197 добавил SQLite-backed approval persistence. Текущий worker pipeline
получает application-level adapter, который отклоняет `Decide` и
`ResolveDeferred`; authenticated controller route продолжает использовать
полный store. Это defense in depth, а не граница изоляции: worker по-прежнему
получает `--db` и доступ к целевой файловой системе, поэтому может обойти
adapter прямой записью в SQLite. Effective isolation отсутствует;
deployment manifests и пилотная приёмка остаются заблокированы до выделения
controller-only API/credentials и отделения worker от writable controller DB.

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

Ответ должен иметь версию схемы, job/run identity, action, bounded artifacts,
digests, exit outcomes и nonce. Controller связывает ответ с выданной capability,
проверяет размер/пути/digests, допускаемые transitions, срок действия и
одноразовость. Структура ответа от worker сама по себе не аттестует честность
модели или проверки; trusted controller записывает, что именно он получил и
какие независимые checks провёл. Decisions для human-gated перехода приходят
только через аутентифицированную session/RBAC control plane и никогда не
выполняются по worker result.

До реализации надо отдельно решить, какие проверки допустимо считать доверенными
при возможной компрометации runner, как возвращать артефакты и как сериализовать
resume. Не переносить в API произвольный filesystem path или команду, выбранную
worker-ом.

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

1. Зафиксировать trust assumptions и модель attestation результата.
2. Спроектировать и протестировать typed worker result / controller commit
   протокол без пересылки человеческих решений как обычных worker outputs.
3. Уже добавлены SQLite approval store, общий persistence port и
   application-level worker adapter, блокирующий штатные вызовы изменения
   решений. Следующий незавершённый шаг — убрать у worker прямой `--db` и
   filesystem доступ и заменить его controller-owned API; отдельно отделить
   evidence от worker scratch с восстановлением существующих runs.
4. После позитивных и негативных протокольных тестов выбрать одну инфраструктуру
   и добавить воспроизводимый reference deployment, TLS, least-purpose secrets,
   persistent storage и backup/restore.
5. Запустить destructive recovery/network/filesystem probes на рабочей
   инфраструктуре; только тогда разрешить `strict` и снять блокер MAJ-07.

Оценка: это не deployment-only изменение. До начала протокольного refactor
нужна отдельная проверка trust model и schema; безопасное объявление текущей
cloud reference конфигурации завершённой невозможно.
