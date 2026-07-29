## Почему

Зафиксированный frontend dependency graph сейчас заставляет и `make verify`,
и GitHub Actions job `frontend` падать на обязательном
`npm audit --audit-level=high`. Из-за этого все последующие изменения лишены
зелёного и воспроизводимого verification baseline, хотя найденная уязвимость
React Router относится к RSC-режиму, который этот SPA не использует.

## Что изменится

- Внешняя зависимость React Router будет заменена минимальным локальным
  browser-history router для трёх существующих SPA routes, поскольку на
  текущую дату npm не предлагает версию React Router без high-severity
  advisories.
- Существующие SPA routes и навигационное поведение сохранятся.
- High-severity dependency audit останется fail-closed: advisory нельзя
  подавлять, исключать или обходить.
- Lockfile и встроенный production frontend будут пересобраны из точного
  проверенного dependency graph.
- Полная локальная и CI-последовательность должна успешно выполнять audit,
  lint, tests, build и проверку актуальности embedded dist.

## Возможности

### Новые возможности

Нет.

### Изменяемые возможности

- `ci-pipeline`: зафиксированный frontend dependency graph должен проходить
  существующий high-severity audit gate и позволять полностью завершить
  frontend verification локально и в CI.

## Влияние

- Frontend dependency declarations и lockfile в `web/`.
- Существующие SPA routes, ссылки, back-navigation и query parameters.
- Пересобранные embedded assets в `web/dist/`.
- Локальный `make verify` и GitHub Actions job `frontend`.
- CLI, HTTP API, persisted data и поведение workflow не изменяются.
