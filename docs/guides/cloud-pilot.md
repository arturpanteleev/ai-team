# Статус облачного пилота

**Статус: поддерживаемого изолированного cloud deployment пока нет.** В текущей
модели нельзя безопасно выдать worker-контейнеру доступ к persistent workspace
и одновременно гарантировать, что он не изменит human approvals или run
evidence. Отдельный процесс, контейнер, read-only root filesystem или закрытый
маршрут к web-порту сами по себе это ограничение не снимают.

## Почему deployment сейчас заблокирован

`ai-team web --scheduler-db` ставит задания в durable очередь. Отдельный
`ai-team scheduler-worker` забирает задание и запускает `ai-team worker`, но
worker исполняет весь pipeline. В scheduler и web worker режимах approvals
хранятся в SQLite `web.db`: controller читает их и принимает решения через
аутентифицированный API. Worker pipeline использует application-level adapter,
который отклоняет штатные вызовы записи решения (`Decide` и
`ResolveDeferred`). Однако worker всё ещё получает путь `--db` и доступ к
целевой файловой системе, поэтому скомпрометированный worker может обойти
adapter прямой записью в SQLite. Это defense in depth, не граница изоляции.
Общий read-write volume до файла базы сохраняет эту возможность.
Lifecycle и run evidence по-прежнему пишутся напрямую в target через
filesystem stores, включая `pkg/evidence.Store`, event log и manifests. Поэтому
worker имеет доступ к обоим классам состояния, которые cloud pilot должен
защищать.

В опциональном Linux bubblewrap режиме `.ai-team/state/candidates` накрывается
отдельным namespace-local tmpfs: controller хранит candidate identity через
run/target-scoped `candidate.metadata.create/read` API, а pipeline использует
тот же worktree для start, resume и recovery. Локальный CLI сохраняет
filesystem store. Эта маска закрывает только metadata directory: candidate
worktree остаётся доступен worker-у для чтения и записи, как и прочие evidence,
artifact и target файлы. Linux CI probe проверяет sentinel в скрытом metadata
каталоге и чтение отдельного worktree sentinel. Это ограниченный slice, не
полная изоляция candidate/artifact state и не закрытие MAJ-07. В облачном
controller-backed режиме resume требует controller-owned candidate metadata и
завершится ошибкой, если она потеряна, независимо от содержимого
worker-writable `run.json`. Поэтому resume non-Git runs в этом режиме пока не
поддерживается; для него нужен отдельный controller-owned маркер подтверждённого
отсутствия кандидата. Локальный CLI сохраняет совместимость со старыми
non-Git runs по provenance из target-файла; этот путь не является trust boundary.

При запуске web/controller файловые approvals из `.ai-team/state/approvals`
импортируются в SQLite транзакционно и без удаления исходных файлов; worker
использует эту базу, но сам импорт не выполняет. Повторный
импорт сохраняет более новую совместимую историю в DB; конфликтующие записи,
некорректная identity или повреждённые файлы останавливают запуск с ошибкой,
чтобы решения людей не терялись при переключении хранилища. Импорт выполняет
controller, а не worker. Это переход данных, но он не меняет границу доступа:
worker по-прежнему может писать в общий DB.

`ProcessEngine` и очередь отделяют процессы и восстанавливают работу после
потери worker. Они не являются OS containment. Переменная `strict` не создаёт
песочницу: pipeline fail-closed отклоняет такой запуск, пока backend
изоляции не реализован. См. также [границу безопасности](../reference/security.md)
и [восстановление worker](worker-recovery.md).

После слияния PR #199 каждый запуск worker получает отдельный `execution_id`,
который должен совпасть в ответе. Дополнительно disposable worker наследует
только ограниченный набор переменных: стандартное окружение запуска плюс
имена из `AI_TEAM_WORKER_ENV_ALLOW`. Его `HOME` и временные XDG-каталоги
создаются отдельно на время задания. Это снижает риск случайной передачи
controller auth/signing/database переменных, но не доказывает отсутствие
секретов в файлах, доступных тому же OS-пользователю, и не создаёт process или
filesystem isolation.

Опциональный Linux-режим `AI_TEAM_WORKER_SANDBOX=bubblewrap` скрывает стандартные
host service sockets под `/run` отдельным tmpfs. Нестандартные pathname AF_UNIX
sockets вне `/run` всё ещё могут быть доступны по host path из-за read-only bind
`/`. Network namespace сохраняет запрет прямого IP egress. Для OpenCode
controller-owned proxy пропускает только HTTPS CONNECT к `api.openai.com:443`
через per-invocation Unix socket с random capability; другие host/port
отвергаются. `HTTPS_PROXY`/`HTTP_PROXY` указывают на namespace-local bridge,
`NO_PROXY` содержит localhost адреса. Proxy не записывает headers, body,
capability или CONNECT destination в logs. Controller также отбрасывает DNS
ответы из частных, shared/carrier-grade NAT, loopback, link-local, documentation,
benchmarking, transition, reserved и других специальных IPv4/IPv6 диапазонов
([IANA IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry.xhtml),
[IANA IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry.xhtml));
публичные unicast-адреса остаются допустимыми. Это консервативный deny-list
специального назначения, а не полноценная проверка BGP-маршрутизации. Linux fake-TLS-upstream probe
проверяет туннель, отказы для других host/port и запрет прямого TCP. Это не
подтверждает реальный OpenAI model round-trip или совместимость всех версий
OpenCode; официальная [сетевая документация OpenCode](https://docs.opencode.ai/docs/network/)
описывает поддержку этих proxy variables, но конкретная установка требует
отдельной проверки с настроенным OpenAI API key. Этот путь покрывает стандартный
OpenAI API endpoint с API key; ChatGPT/Codex OAuth и custom `baseURL` на другом
host не разрешены. Если scoped API/egress Unix socket нельзя создать или
bubblewrap не запускается, worker завершается без fallback. API key по-прежнему
передаётся только явным `AI_TEAM_WORKER_ENV_ALLOW=OPENAI_API_KEY`; isolated
`HOME` не наследует OpenCode `auth.json`.

## Минимальная архитектурная граница

Следующее изменение должно разделить controller-owned state и worker-owned
вычисления до добавления production deployment manifests. Перенос approvals в
SQLite и общий storage port упрощают подключение controller storage, но не
удовлетворяют этот gate, пока worker имеет доступ к той же базе:

1. Контроллер остаётся единственным владельцем admission, очереди, lifecycle,
   human decisions, authoritative evidence, credentials доставки и внешних
   control API.
2. Исполнитель получает только конкретное задание, candidate workspace и
   явно назначенные runtime credentials. Он не монтирует `.ai-team`, базу
   control plane, секрет аутентификации, каталог approvals/evidence, socket
   контейнерного runtime или delivery credentials.
3. Worker отправляет controller-у ограниченный результат по отдельному
   job-scoped протоколу. Capability привязана к job/run, допустимому действию,
   сроку и nonce; она не может создавать human decisions или вызывать
   административные операции. Повтор, другой run, истёкшая capability,
   невалидная схема или чрезмерный output отклоняются.
4. Контроллер сам проверяет результат и записывает authoritative evidence.
   Неподписанные поля результата worker не трактуются как доказательство
   решения человека или успешной проверки. Delivery запускает только
   controller после собственной проверки exact approved plan.
5. Только после подтверждения этой границы выбирается и фиксируется один
   поддерживаемый runtime/provider для reference deployment. В нём отдельно
   задаются TLS ingress, restart/health policy, persistent controller storage,
   least-purpose secrets, backup/restore и deny-by-default network policy.

## Threat model и критерии, которые должны блокировать release

Считаем worker потенциально скомпрометированным через агент, проектный код,
проверку или зависимость. Он может выполнять произвольные операции с доступными
ему файлами и сетью в пределах runtime policy. Защищаемые активы — решения
людей, журнал и артефакты evidence, состояние очереди/control plane, секреты
аутентификации и полномочия публикации. Внешний ingress — единственная точка
для браузеров; worker не должен иметь маршрут к административному API.

Перед объявлением пилота поддерживаемым необходимы воспроизводимые негативные
проверки, а не только просмотр YAML:

- запущенный worker не может прочитать секрет control plane, изменить или
  удалить approval/decision, lifecycle или уже записанное evidence; попытка
  завершается отказом ОС/API, после чего `ai-team verify` подтверждает
  неизменность сохранённого evidence;
- worker не может соединиться с control/admin HTTP endpoints, даже если знает
  URL; ingress policy допускает браузерный HTTPS, но не worker-originated
  административный запрос;
- worker с корректной capability может отправить результат только своему
  активному job; неверный run/action, повторный nonce, истечение, подмена
  approval, невалидный/слишком большой output и неизвестные поля отклоняются;
- после убийства worker, рестарта control plane и восстановления согласованной
  резервной копии run, approval и evidence остаются доступны и проверяемы;
- проверяется фактический запуск двух browser identities и отказ от
  неавторизованного действия; объявленный статус containment совпадает с
  реально применённой OS/network policy.

До выполнения этих критериев разрешён только локальный/доверенный сценарий с
учётом существующей модели доверия. Не размещайте этот worker как sandbox для
недоверенного кода или пользователей. Это архитектурный блокер MAJ-07, а не
описание уже доступной deployment-конфигурации.
