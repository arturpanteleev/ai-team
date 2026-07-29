## 1. Queue и leases

- [x] 1.1 Реализовать persistent schema и строгий enqueue worker jobs
- [x] 1.2 Реализовать atomic claim/renew/complete с ownership token
- [x] 1.3 Добавить lease expiry recovery, idempotency и cancel signal
- [x] 1.4 Применить global/per-target concurrency limits

## 2. Distributed execution

- [x] 2.1 Реализовать queue-backed RunEngine для control plane
- [x] 2.2 Добавить scheduler poller с heartbeat и disposable ProcessEngine
- [x] 2.3 Добавить CLI flags/commands web enqueue и external worker

## 3. Artifact storage

- [x] 3.1 Ввести BlobStore и local SHA-256 CAS
- [x] 3.2 Архивировать immutable run evidence в проверяемый manifest
- [x] 3.3 Добавить restore/verification tests

## 4. Завершение

- [x] 4.1 Добавить concurrency, stale lease, restart и integration tests
- [x] 4.2 Обновить русскую документацию
- [x] 4.3 Выполнить strict validation и `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
