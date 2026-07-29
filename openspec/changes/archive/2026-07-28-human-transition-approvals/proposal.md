# Обязательные человеческие решения на переходах

## Why

Сейчас решение человека связано с TTY: без интерактивного terminal checkpoint
останавливает процесс, а review loopback вообще не создаётся. Для web/cloud
человеческое решение должно быть долговечной доменной сущностью, относящейся к
точному предлагаемому переходу и содержимому.

## What Changes

- Вводятся typed `PendingApproval` и `ApprovalDecision`.
- Approval хранит run/attempt, предыдущую и следующую стадии, exact subject
  hash, допустимые actions, требуемые роли и quorum.
- Решение хранит actor identity, роль, action, комментарий и timestamp.
- Non-interactive pipeline сохраняет pending approval и переводит тот же run в
  `waiting`; TTY и CLI становятся лишь transport-ами решения.
- Resume проверяет решение, его роль и subject hash, затем продолжает
  сохранённый переход без повторного AI-stage.
- `CHANGES_REQUESTED`/`REJECTED` создаёт человеческий выбор
  `return_to_coder | reject | request_information | override_approve`.
- Config schema v3 получает явные transitional approval roles; default
  workflow требует человека после каждого смыслового AI-stage.

## Impact

- Новый package approval, lifecycle state, pipeline/CLI/config и evidence.
- Capabilities: новый `human-approvals`; изменённые `pipeline-gates`,
  `workflow-loopback`, `run-evidence`, `cli-interface`, `role-config`.
