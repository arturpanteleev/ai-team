# Изолированный candidate worktree

## Why

Сейчас агенты, checks и delivery работают прямо в пользовательском checkout.
Snapshot guards обнаруживают лишние изменения постфактум, но review,
approvals и delivery всё ещё относятся к изменяемому live workspace.
Нужен один физически отделённый candidate на run без преждевременной
OS-specific sandbox-платформы.

## What Changes

- Каждый новый Git run создаёт detached worktree от exact baseline commit.
- Resume открывает тот же worktree и проверяет его run/base identity.
- Agent runtime, deterministic checks, review, candidate evidence и delivery
  используют worktree как единственный source root.
- Control state, approvals, logs и immutable run evidence остаются в
  `.ai-team` live target и переживают restart.
- Canonical candidate identity содержит base commit/tree, workspace hash,
  patch hash, changed files, checks и attempts.
- Human approval subject включает candidate hash; изменение candidate делает
  прежний subject stale.
- Delivery plan и executed checks обязаны ссылаться на тот же candidate hash.
- Live source checkout не изменяется; отдельная promotion не выполняется
  неявно.

## Impact

- Новый capability `candidate-isolation`.
- Изменяются `control-plane-safety`, `deterministic-checks`,
  `delivery-executor`, `run-evidence`, `human-approvals` и `e2e-testing`.
- Затрагиваются pipeline target routing, Git lifecycle и evidence.
