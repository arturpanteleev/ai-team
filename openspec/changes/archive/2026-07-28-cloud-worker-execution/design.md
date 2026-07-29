# Design: Disposable worker

## Protocol

Control plane создаёт `Job` schema v1 с operation, run identity, exact target
mount и параметрами start/resume/cancel. Job передаётся worker через stdin как
один bounded JSON document. Неизвестные поля, несовместимые параметры и
другой target отклоняются fail-closed.

## Process engine

`ProcessEngine` реализует тот же интерфейс, что `pipeline.RunEngine`, но
запускает настроенную argv-команду с subcommand `worker`. Context cancellation
убивает process; повторный отдельный cancel job завершает persisted lifecycle.
Вывод bounded и используется только для diagnostics.

`ai-team worker` загружает config и layered agent registry внутри mounted
repository, подключает recorder к общей SQLite projection и вызывает обычный
RunEngine. Поэтому approvals, candidate identity, checks и delivery не имеют
отдельной «облачной» реализации.

## Isolation boundary

Worker не обещает OS isolation самостоятельно. Контракт требует, чтобы cloud
launcher создавал disposable job/container и монтировал только exact target и
необходимые credentials. Локальный subprocess — reference launcher и
development fallback.
