# Web control plane для run и человеческих решений

## Why

Web-интерфейс уже показывает live-состояние через WebSocket, но остаётся
read-only: из браузера нельзя создать run, принять обязательное человеческое
решение, отменить или продолжить выполнение. Облачная модель требует, чтобы
TTY и HTTP были равноправными transport-ами одного `RunEngine`.

## What Changes

- Добавляется асинхронный controller над `RunEngine` с start/resume/cancel.
- Добавляются write API для runs и approval decisions.
- Read API возвращает pending approvals, actions, roles, quorum, exact subject
  и историю решений.
- Локальный web получает случайную session-cookie и отдельный CSRF token;
  write API проверяет оба значения и Origin/Host.
- Actor identity, role, action и comment из browser decision проходят общую
  approval validation.
- Dashboard и run detail получают формы запуска, pending approvals и
  управляющие действия.

## Impact

- Новый capability `web-run-control`.
- Изменяются `web-api`, `web-http-server`, `web-dashboard`,
  `web-pipeline-detail`.
- Затрагиваются RunEngine, local controller, HTTP server и frontend.
