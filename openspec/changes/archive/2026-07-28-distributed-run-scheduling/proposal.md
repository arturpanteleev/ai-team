# Change: Распределённая очередь run и persistent artifact storage

## Why

Disposable worker отделён от web process, но control plane всё ещё запускает
его напрямую. После рестарта теряется process-local dispatch, несколько
control-plane replicas могут запустить один run дважды, а immutable evidence
остаётся только на repository volume.

## What Changes

- вводится persistent SQLite queue с атомарным claim и idempotency;
- worker получает bounded lease, heartbeat и fail-closed ownership token;
- scheduler применяет global/per-target concurrency limits и cancel signal;
- отдельный poller исполняет claimed job через disposable worker launcher;
- immutable run evidence архивируется в content-addressed artifact storage с
  проверяемым manifest;
- web может enqueue jobs, не исполняя AI runtime и не владея worker process.

## Вне scope

- конкретный managed queue/object-storage vendor;
- Kubernetes operator и autoscaling policy;
- cross-region replication и billing quotas.
