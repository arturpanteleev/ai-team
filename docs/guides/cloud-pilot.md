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
