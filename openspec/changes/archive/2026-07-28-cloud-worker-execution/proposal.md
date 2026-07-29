# Change: Исполнение run в disposable worker

## Why

Web control plane сейчас выполняет AI pipeline внутри собственного процесса.
Это связывает HTTP lifecycle, credentials и filesystem execution и не
позволяет облачной платформе изолировать run отдельным job/container.

## What Changes

- вводится строгий versioned worker job protocol для start/resume/cancel;
- control plane получает process-backed RunEngine;
- добавляется `ai-team worker`, исполняющий ровно один job в mounted target;
- worker пишет существующие evidence, lifecycle, SQLite и WebSocket stream;
- infrastructure launcher может заменить локальный subprocess disposable
  container-ом без изменения pipeline и approvals.

## Вне scope

- собственный OS sandbox backend, quotas и network policy;
- распределённая очередь, leases и object storage — следующий change;
- Kubernetes/Docker-specific SDK.
