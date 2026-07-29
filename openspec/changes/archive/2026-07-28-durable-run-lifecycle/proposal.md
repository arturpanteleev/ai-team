# Долговечный жизненный цикл run

## Зачем

Сейчас pipeline привязан к одному процессу: restart создаёт новый run, а
`--retry-from` переиспользует live-артефакты, но не продолжает прежнюю
identity. Это несовместимо с web/cloud approvals, где run должен безопасно
ждать решение человека и продолжаться другим worker-процессом.

## Что меняется

- Вводится controller-owned persisted state конкретного run.
- `RunEngine` разделяет создание нового run и возобновление существующего.
- После каждого подтверждённого перехода состояние атомарно фиксирует текущую
  позицию, attempt ordinal и следующий допустимый этап.
- CLI получает `run --resume <run_id>` и продолжает ту же run identity.
- Immutable event chain открывается для append после проверки целостности;
  новый `run_started` при resume не создаётся, пишется `run_resumed`.
- Resume fail-closed проверяет feature, target, config/workflow snapshots и
  наличие ожидаемых inputs.

## Затронутые области

- `pkg/pipeline`, новый lifecycle package, `pkg/evidence`, recorder/SQLite,
  `cmd/ai-team`.
- Capabilities: новый `run-lifecycle`; изменённые `workflow-engine`,
  `workflow-rollback`, `cli-interface`, `run-evidence`.
