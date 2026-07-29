# Design: Queue, leases и artifacts

## Persistent queue

Schema хранит versioned worker job, status, attempt count, timestamps,
lease owner/token/expiry, cancel flag и bounded error. Enqueue имеет unique
active identity `(run_id, operation)`. Claim выполняется conditional SQL
update: только queued job, только при свободном global slot и отсутствии
другого running job для exact target.

Истёкшие leases возвращаются в queued перед новым claim. Renew и Complete
требуют exact worker ID и случайный lease token; stale worker не может
завершить переclaimed job.

## Poller

`scheduler-worker` claim-ит один job, запускает существующий ProcessEngine и
продлевает lease. Cancel flag отменяет context disposable process и завершает
persisted run через отдельный cancel job/operation. Режим `--once` удобен для
platform job; loop mode — для long-lived development worker.

## Artifact storage

После worker job poller обходит только immutable `.ai-team/runs/{run_id}`,
отклоняет symlink/non-regular entries, кладёт содержимое в SHA-256 CAS и
публикует manifest path/digest/size. Manifest позволяет восстановить и
проверить evidence независимо от ephemeral worker filesystem. Local CAS —
reference implementation интерфейса будущего object storage.
