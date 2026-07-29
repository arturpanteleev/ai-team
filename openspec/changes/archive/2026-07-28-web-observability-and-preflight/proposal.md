# Наблюдаемость run и проверка окружения

## Why

Web уже управляет долговечным run, но оператор не видит live-лог текущего
агента и controller-owned evidence проверок, мутаций и delivery. Ошибки
окружения OpenCode, Git и GitHub обнаруживаются только после принятия run,
поэтому человек может выдать approval для заведомо неисполнимого перехода.

## What Changes

- Добавляется типизированный runtime preflight с обязательными и
  диагностическими проверками.
- Preflight проверяет OpenCode и его версию, выбранную model/provider,
  credential allow-list, Git repository, текущую ветку, remote и, когда
  workflow содержит delivery, доступность и authentication `gh`.
- Web показывает readiness до создания run, а start отклоняет запуск при
  обязательной неуспешной проверке.
- Добавляется ограниченный read API immutable live-логов без доступа к
  произвольным путям.
- Run detail показывает checks, mutations и delivery evidence непосредственно
  из SQLite projection, а live-лог обновляет только во время активного attempt.

## Impact

- Новый capability `runtime-preflight`.
- Изменяются `web-pipeline-detail`, `web-artifact-viewer`,
  `opencode-integration`, `web-api` и `web-dashboard`.
- Затрагиваются local controller, HTTP server и frontend.
